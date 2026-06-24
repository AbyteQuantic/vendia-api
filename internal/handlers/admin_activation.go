// Spec: specs/073-activacion-tiendas-gtm/gtm.md
//
// Tablero de activación de las primeras tiendas (god-mode). Convierte las
// consultas que el fundador re-corría a mano cada semana (gtm.md §Tablero)
// en un endpoint vivo que la página admin-web /admin/activation consume.
//
// El cuello de botella del negocio es distribución+retención, no features.
// Esta es la herramienta #1 para medir el embudo y decidir a quién llamar.
package handlers

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// activationFunnel — las 5 etapas del embudo (gtm.md §Diagnóstico).
type activationFunnel struct {
	Registradas  int64 `json:"registradas"`
	Onboardeadas int64 `json:"onboardeadas"`
	Activas7d    int64 `json:"activas_7d"`
	Activas28d   int64 `json:"activas_28d"`
	Pagas        int64 `json:"pagas"`
}

// activationTenantRow — una fila del desglose "a quién llamar".
type activationTenantRow struct {
	TenantID          string `json:"tenant_id"`
	Tienda            string `json:"tienda"`
	Productos         int64  `json:"productos"`
	VentasTotal       int64  `json:"ventas_total"`
	UltimaVenta       string `json:"ultima_venta"` // YYYY-MM-DD o "" si nunca
	Fiados            int64  `json:"fiados"`
	Plan              string `json:"plan"`
	DiasDesdeRegistro int    `json:"dias_desde_registro"`
}

type activationResponse struct {
	Funnel  activationFunnel      `json:"funnel"`
	Tiendas []activationTenantRow `json:"tiendas"`
}

// isSeedTenant excluye los datos de prueba del modelo de proveedores
// (Seed 074), cuyos UUID llevan el prefijo "5eed". Mismo criterio que el
// `id::text NOT LIKE '5eed%'` del tablero SQL de gtm.md.
func isSeedTenant(id string) bool {
	return strings.HasPrefix(strings.ToLower(id), "5eed")
}

// AdminActivationFunnel — GET /api/v1/admin/activation (super_admin).
//
// Computa el embudo y el desglose por tienda en memoria (NO con
// date_trunc/subqueries SQL) para mantener portabilidad con SQLite en los
// tests, igual que AdminAnalytics. El volumen es de unas pocas decenas de
// tenants: cabe holgado en memoria.
func AdminActivationFunnel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now().UTC()
		since7 := now.Add(-7 * 24 * time.Hour)
		since28 := now.Add(-28 * 24 * time.Hour)

		// 1. Tenants vivos (sin los de semilla).
		type tenantRow struct {
			ID                 string
			BusinessName       string
			SubscriptionStatus string
			CreatedAt          time.Time
		}
		var tenants []tenantRow
		db.Model(&models.Tenant{}).
			Select("id, business_name, subscription_status, created_at").
			Where("deleted_at IS NULL").
			Scan(&tenants)

		// 2. Productos vivos agrupados por tenant.
		productCount := groupCount(db, &models.Product{}, "tenant_id", "deleted_at IS NULL")

		// 3. Ventas: total, última fecha y banderas 7d/28d por tenant.
		type saleRow struct {
			TenantID  string
			CreatedAt time.Time
		}
		var sales []saleRow
		db.Model(&models.Sale{}).
			Select("tenant_id, created_at").
			Where("deleted_at IS NULL").
			Scan(&sales)

		salesTotal := map[string]int64{}
		lastSale := map[string]time.Time{}
		soldIn7d := map[string]bool{}
		soldIn28d := map[string]bool{}
		for _, s := range sales {
			salesTotal[s.TenantID]++
			if s.CreatedAt.After(lastSale[s.TenantID]) {
				lastSale[s.TenantID] = s.CreatedAt
			}
			if s.CreatedAt.After(since7) {
				soldIn7d[s.TenantID] = true
			}
			if s.CreatedAt.After(since28) {
				soldIn28d[s.TenantID] = true
			}
		}

		// 4. Fiados: total por tenant + bandera 28d.
		type creditRow struct {
			TenantID  string
			CreatedAt time.Time
		}
		var credits []creditRow
		db.Model(&models.CreditAccount{}).
			Select("tenant_id, created_at").
			Where("deleted_at IS NULL").
			Scan(&credits)

		fiadoCount := map[string]int64{}
		fiadoIn28d := map[string]bool{}
		for _, cr := range credits {
			fiadoCount[cr.TenantID]++
			if cr.CreatedAt.After(since28) {
				fiadoIn28d[cr.TenantID] = true
			}
		}

		// 5. Ensamblar embudo + desglose.
		var funnel activationFunnel
		rows := make([]activationTenantRow, 0, len(tenants))
		for _, t := range tenants {
			if isSeedTenant(t.ID) {
				continue
			}
			funnel.Registradas++
			prods := productCount[t.ID]
			if prods > 0 {
				funnel.Onboardeadas++
			}
			if soldIn7d[t.ID] {
				funnel.Activas7d++
			}
			if soldIn28d[t.ID] || fiadoIn28d[t.ID] {
				funnel.Activas28d++
			}
			if t.SubscriptionStatus == "active" {
				funnel.Pagas++
			}

			ultima := ""
			if ls, ok := lastSale[t.ID]; ok && !ls.IsZero() {
				ultima = ls.UTC().Format("2006-01-02")
			}
			rows = append(rows, activationTenantRow{
				TenantID:          t.ID,
				Tienda:            t.BusinessName,
				Productos:         prods,
				VentasTotal:       salesTotal[t.ID],
				UltimaVenta:       ultima,
				Fiados:            fiadoCount[t.ID],
				Plan:              t.SubscriptionStatus,
				DiasDesdeRegistro: int(now.Sub(t.CreatedAt).Hours() / 24),
			})
		}

		// Orden: el más activo arriba (ventas, luego productos) — el mismo
		// criterio del ORDER BY del tablero, para que la fila de "máximo
		// esfuerzo / cero ajá" (muchos productos, 0 ventas) salte a la vista.
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].VentasTotal != rows[j].VentasTotal {
				return rows[i].VentasTotal > rows[j].VentasTotal
			}
			return rows[i].Productos > rows[j].Productos
		})

		c.JSON(http.StatusOK, activationResponse{Funnel: funnel, Tiendas: rows})
	}
}

// groupCount devuelve count(*) agrupado por [col] para [model] bajo [where].
// Helper local para los agregados del tablero de activación.
func groupCount(db *gorm.DB, model any, col, where string) map[string]int64 {
	type row struct {
		Key string
		N   int64
	}
	var rows []row
	db.Model(model).
		Select(col+" AS key, count(*) AS n").
		Where(where).
		Group(col).
		Scan(&rows)
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Key] = r.N
	}
	return out
}
