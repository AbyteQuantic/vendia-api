// Hotfix 2026-05-31: vendia.store/admin/analytics consume este
// endpoint con 6 secciones agregadas — el frontend (admin-web
// AnalyticsPage + types.AnalyticsData) lo espera desde hace tiempo
// pero nunca se construyó en el backend → 404 → "No se pudieron
// cargar las analíticas".
//
// Las sub-rutas /admin/analytics/overview/ai-costs/revenue/profitability
// existen y devuelven shapes distintas (cada una para otra pantalla
// del admin). Esta es la agregadora que la página /admin/analytics
// pide directo en /admin/analytics?days=N.
package handlers

import (
	"net/http"
	"strconv"
	"time"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// adminAnalyticsResponse es el contrato exacto que admin-web lee vía
// `types/admin.AnalyticsData`. JSON tags fijos por campo para no
// arrastrar renombres silenciosos.
type adminAnalyticsResponse struct {
	SalesTrend           []analyticsTrendPoint   `json:"sales_trend"`
	PaymentMethods       []analyticsPaymentRow   `json:"payment_methods"`
	SalesByBusinessType  []analyticsBizTypeRow   `json:"sales_by_business_type"`
	OnlineVsOffline      analyticsOnlineOffline  `json:"online_vs_offline"`
	TopProducts          []analyticsTopProduct   `json:"top_products"`
	ActivityHeatmap      []analyticsHeatmapCell  `json:"activity_heatmap"`
}

type analyticsTrendPoint struct {
	Date         string  `json:"date"`
	Total        float64 `json:"total"`
	Transactions int64   `json:"transactions"`
}

type analyticsPaymentRow struct {
	Method string  `json:"method"`
	Count  int64   `json:"count"`
	Total  float64 `json:"total"`
}

type analyticsBizTypeRow struct {
	Type  string  `json:"type"`
	Total float64 `json:"total"`
}

type analyticsOnlineOffline struct {
	Online  int64 `json:"online"`
	Offline int64 `json:"offline"`
}

type analyticsTopProduct struct {
	Name     string `json:"name"`
	Quantity int64  `json:"quantity"`
}

type analyticsHeatmapCell struct {
	Hour  int   `json:"hour"`
	Day   int   `json:"day"` // 0 = domingo, 1 = lunes, ...
	Count int64 `json:"count"`
}

// AdminAnalytics es GET /api/v1/admin/analytics?days=N (super_admin).
// Default 7 días, max 90. Devuelve las 6 secciones agregadas.
func AdminAnalytics(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		days := parseDaysParam(c.Query("days"))
		// Trabajamos en UTC para consistencia entre el cálculo de
		// `since`, el bucketing por día en collectSalesTrend, y la
		// fecha-cabecera del zero-fill. Mezclar local/UTC introducía
		// un off-by-one en el límite de medianoche.
		now := time.Now().UTC()
		since := now.Add(-time.Duration(days) * 24 * time.Hour)

		var resp adminAnalyticsResponse

		resp.SalesTrend = collectSalesTrend(db, since, now, days)
		resp.PaymentMethods = collectPaymentMethods(db, since)
		resp.SalesByBusinessType = collectSalesByBusinessType(db, since)
		resp.OnlineVsOffline = collectOnlineOffline(db, now)
		resp.TopProducts = collectTopProducts(db, since)
		resp.ActivityHeatmap = collectActivityHeatmap(db, since)

		c.JSON(http.StatusOK, resp)
	}
}

// parseDaysParam clampea ?days entre 1 y 90; default 7.
func parseDaysParam(raw string) int {
	if raw == "" {
		return 7
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 7
	}
	if n > 90 {
		return 90
	}
	return n
}

// collectSalesTrend agrupa por día (UTC) los últimos N días.
// Zero-fill para días sin ventas → el LineChart del frontend no
// rompe la línea en gaps.
//
// Bucketeamos en Go (NO con DATE_TRUNC) para mantener portabilidad
// con SQLite en tests. Volumen es bajo: N días de ventas para
// todos los tenants caben en memoria sin problema.
func collectSalesTrend(db *gorm.DB, since, now time.Time, days int) []analyticsTrendPoint {
	type saleRow struct {
		CreatedAt time.Time
		Total     float64
	}
	var rows []saleRow
	db.Model(&models.Sale{}).
		Select("created_at, total").
		Where("created_at >= ? AND deleted_at IS NULL", since).
		Scan(&rows)

	byDate := map[string]analyticsTrendPoint{}
	for _, r := range rows {
		d := r.CreatedAt.UTC().Format("2006-01-02")
		pt := byDate[d]
		pt.Date = d
		pt.Total += r.Total
		pt.Transactions++
		byDate[d] = pt
	}

	out := make([]analyticsTrendPoint, 0, days)
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for i := days - 1; i >= 0; i-- {
		d := startOfToday.Add(-time.Duration(i) * 24 * time.Hour).Format("2006-01-02")
		if pt, ok := byDate[d]; ok {
			out = append(out, pt)
		} else {
			out = append(out, analyticsTrendPoint{Date: d})
		}
	}
	return out
}

func collectPaymentMethods(db *gorm.DB, since time.Time) []analyticsPaymentRow {
	rows := []analyticsPaymentRow{}
	db.Model(&models.Sale{}).
		Select(`payment_method AS method,
		        COUNT(*) AS count,
		        COALESCE(SUM(total), 0) AS total`).
		Where("created_at >= ? AND deleted_at IS NULL", since).
		Group("payment_method").
		Order("total DESC").
		Scan(&rows)
	return rows
}

func collectSalesByBusinessType(db *gorm.DB, since time.Time) []analyticsBizTypeRow {
	// business_types es array JSON en tenants. Bucketing en Go para
	// no enredarse con dialectos SQL — load tenants + ventas por
	// tenant y agregamos acá.
	type saleAgg struct {
		TenantID string
		Total    float64
	}
	agg := []saleAgg{}
	db.Model(&models.Sale{}).
		Select("tenant_id, COALESCE(SUM(total), 0) AS total").
		Where("created_at >= ? AND deleted_at IS NULL", since).
		Group("tenant_id").
		Scan(&agg)

	tenantsByID := map[string]models.Tenant{}
	if len(agg) > 0 {
		ids := make([]string, 0, len(agg))
		for _, a := range agg {
			ids = append(ids, a.TenantID)
		}
		var tenants []models.Tenant
		db.Select("id, business_types").
			Where("id IN ?", ids).
			Find(&tenants)
		for _, t := range tenants {
			tenantsByID[t.ID] = t
		}
	}

	bucket := map[string]float64{}
	for _, a := range agg {
		key := "desconocido"
		if t, ok := tenantsByID[a.TenantID]; ok &&
			len(t.BusinessTypes) > 0 && t.BusinessTypes[0] != "" {
			key = t.BusinessTypes[0]
		}
		bucket[key] += a.Total
	}

	out := make([]analyticsBizTypeRow, 0, len(bucket))
	for typ, total := range bucket {
		out = append(out, analyticsBizTypeRow{Type: typ, Total: total})
	}
	return out
}

// collectOnlineOffline: tenant es "online" si sincronizó en la última
// hora. Sin last_sync_at = nunca sincronizó = offline.
func collectOnlineOffline(db *gorm.DB, now time.Time) analyticsOnlineOffline {
	oneHourAgo := now.Add(-1 * time.Hour)
	var online, total int64
	db.Model(&models.Tenant{}).Where("deleted_at IS NULL").Count(&total)
	db.Model(&models.Tenant{}).
		Where("deleted_at IS NULL AND last_sync_at IS NOT NULL AND last_sync_at >= ?", oneHourAgo).
		Count(&online)
	return analyticsOnlineOffline{Online: online, Offline: total - online}
}

// collectTopProducts: top 10 por cantidad vendida en la ventana.
// Usa el name del SaleItem (snapshot al momento de la venta) para
// no depender de que el producto siga existiendo.
func collectTopProducts(db *gorm.DB, since time.Time) []analyticsTopProduct {
	rows := []analyticsTopProduct{}
	db.Table("sale_items AS si").
		Select("si.name AS name, COALESCE(SUM(si.quantity), 0) AS quantity").
		Joins("JOIN sales s ON s.id = si.sale_id").
		Where("s.created_at >= ? AND s.deleted_at IS NULL AND si.deleted_at IS NULL AND si.is_service = false", since).
		Group("si.name").
		Order("quantity DESC").
		Limit(10).
		Scan(&rows)
	return rows
}

// collectActivityHeatmap: COUNT(ventas) por (día_de_semana, hora).
// day=0 domingo ... day=6 sábado. hour=0..23.
// Solo emite celdas con count>0 para que el frontend renderee
// menos puntos.
func collectActivityHeatmap(db *gorm.DB, since time.Time) []analyticsHeatmapCell {
	// Bucketeamos en Go para portabilidad SQLite/Postgres.
	type saleRow struct {
		CreatedAt time.Time
	}
	var rows []saleRow
	db.Model(&models.Sale{}).
		Select("created_at").
		Where("created_at >= ? AND deleted_at IS NULL", since).
		Scan(&rows)

	type cellKey struct{ day, hour int }
	counts := map[cellKey]int64{}
	for _, r := range rows {
		t := r.CreatedAt.UTC()
		k := cellKey{day: int(t.Weekday()), hour: t.Hour()}
		counts[k]++
	}

	out := make([]analyticsHeatmapCell, 0, len(counts))
	for k, n := range counts {
		out = append(out, analyticsHeatmapCell{Hour: k.hour, Day: k.day, Count: n})
	}
	return out
}
