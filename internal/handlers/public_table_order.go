// Spec: specs/083-mesas-catalogo-qr/spec.md
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PublicAddItemsToTableTab — POST /api/v1/public/catalog/:slug/table/:id/order
//
// Pedido de mesa por QR (Spec 083, decisión fundador 2026-06-27): UNIFICADO con
// la "cuenta de mesa" (OrderTicket). El comensal escanea el QR de la mesa
// (`?mesa=<id>` → esta ruta) y su pedido ABRE o AGREGA a la cuenta abierta de
// esa mesa, igual que el mesero desde el POS. Así:
//   - La mesa queda OCUPADA (ticket abierto) hasta que se cobre.
//   - El inventario se descuenta al CERRAR/COBRAR (flujo normal del POS).
//   - Aparece en el Centro de Tareas como cuenta por cobrar.
//   - Soporta abonos/pagos por el session_token devuelto.
//
// Público (sin auth) pero acotado: requiere un table_id real y ACTIVO del tenant
// del slug (el id viaja en el QR que imprimió el tendero). Sin descuento de
// stock aquí (Spec 052: el tab es borrador hasta cobrar).
// isRetryableConflict detecta, de forma portable, los errores transitorios de
// concurrencia por los que vale la pena REINTENTAR la apertura de la cuenta de
// mesa: violación de índice único (otro pedido ganó la carrera → el reintento
// acumula) y fallos de bloqueo/serialización (Postgres 40001/40P01, sqlite
// "database is locked"). Council BUG-DUP-ACCOUNT-RACE Spec 083.
func isRetryableConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"unique constraint", "duplicate key", "23505", "unique",
		"database is locked", "database table is locked", "busy",
		"40001", "40p01", "serializ", "deadlock",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// sumItems calcula el total de una cuenta como la suma exacta de sus renglones.
// Única fuente de verdad del Total (evita drift Total vs líneas).
func sumItems(items []models.OrderItem) float64 {
	var t float64
	for _, it := range items {
		t += it.UnitPrice * float64(it.Quantity)
	}
	return t
}

func PublicAddItemsToTableTab(db *gorm.DB) gin.HandlerFunc {
	type ItemReq struct {
		ProductID string  `json:"product_id"`
		Name      string  `json:"name" binding:"required"`
		Quantity  int     `json:"quantity" binding:"required,min=1"`
		Price     float64 `json:"price" binding:"required,gt=0"`
	}
	type Request struct {
		Items        []ItemReq `json:"items" binding:"required,min=1"`
		CustomerName string    `json:"customer_name"`
		// PlacedBy etiqueta quién tomó el pedido ("cliente" | "mesero"); informativo.
		PlacedBy string `json:"placed_by"`
	}

	return func(c *gin.Context) {
		slug := c.Param("slug")
		tableID := c.Param("id")

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		var table models.Table
		if err := db.Where("id = ? AND tenant_id = ? AND is_active = ?", tableID, tenant.ID, true).
			First(&table).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "mesa no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		label := table.Label
		branchID := defaultBranchForTenant(db, tenant.ID)

		// Pre-agregar los ítems del request por product_id (council BUG-DUP-NEWITEM):
		// dos líneas del MISMO producto en un request se funden en una, así no se
		// crean filas duplicadas ni se pierde el match contra la cuenta existente.
		type aggItem struct {
			name  string
			qty   int
			price float64
		}
		ordered := make([]string, 0, len(req.Items)) // preserva orden estable
		agg := map[string]*aggItem{}
		var adhoc []aggItem // ítems sin product_id (nunca se fusionan)
		for _, it := range req.Items {
			pid := strings.TrimSpace(it.ProductID)
			if pid == "" {
				adhoc = append(adhoc, aggItem{name: it.Name, qty: it.Quantity, price: it.Price})
				continue
			}
			if a, ok := agg[pid]; ok {
				a.qty += it.Quantity
				a.price = it.Price // último precio gana
				a.name = it.Name
			} else {
				agg[pid] = &aggItem{name: it.Name, qty: it.Quantity, price: it.Price}
				ordered = append(ordered, pid)
			}
		}

		var result models.OrderTicket
		// Retry anti-carrera (council BUG-DUP-ACCOUNT-RACE): si dos primeros
		// pedidos concurrentes a la misma mesa intentan crear la cuenta, el índice
		// único parcial (tenant_id,label,abierto) rechaza al perdedor; reintentamos
		// → el 2º intento encuentra la cuenta del ganador y ACUMULA (no se pierde
		// el pedido ni se duplica la cuenta).
		var txErr error
		for attempt := 0; attempt < 3; attempt++ {
			txErr = db.Transaction(func(tx *gorm.DB) error {
				var existing models.OrderTicket
				// Lock pesimista REAL (clause.Locking; el viejo Set("gorm:query_option")
				// era no-op en GORM v2 → council BUG-RACE).
				err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Preload("Items").
					Where("tenant_id = ? AND label = ? AND status IN ?",
						tenant.ID, label, []models.OrderStatus{
							models.OrderStatusNuevo,
							models.OrderStatusPreparando,
							models.OrderStatusListo,
						}).
					Order("created_at DESC").
					First(&existing).Error

				if errors.Is(err, gorm.ErrRecordNotFound) {
					// No hay cuenta abierta → se crea (mesa ocupada).
					var items []models.OrderItem
					for _, pid := range ordered {
						a := agg[pid]
						items = append(items, models.OrderItem{
							ProductUUID: pid, ProductName: a.name,
							Quantity: a.qty, UnitPrice: a.price,
						})
					}
					for _, a := range adhoc {
						items = append(items, models.OrderItem{
							ProductName: a.name, Quantity: a.qty, UnitPrice: a.price,
						})
					}
					created := models.OrderTicket{
						TenantID:     tenant.ID,
						BranchID:     branchID,
						Label:        label,
						CustomerName: strings.TrimSpace(req.CustomerName),
						Status:       models.OrderStatusNuevo,
						Type:         models.OrderTypeMesa,
						Total:        sumItems(items),
						Items:        items,
					}
					if err := tx.Create(&created).Error; err != nil {
						return err
					}
					result = created
					return nil
				}
				if err != nil {
					return err
				}

				// Cuenta existente → acumular (Spec 052: sin tocar stock).
				for _, pid := range ordered {
					a := agg[pid]
					found := false
					for idx, oi := range existing.Items {
						if oi.ProductUUID == pid {
							if err := tx.Model(&existing.Items[idx]).
								Update("quantity", oi.Quantity+a.qty).Error; err != nil {
								return err
							}
							found = true
							break
						}
					}
					if !found {
						if err := tx.Create(&models.OrderItem{
							OrderUUID:   existing.ID,
							ProductUUID: pid, ProductName: a.name,
							Quantity: a.qty, UnitPrice: a.price,
						}).Error; err != nil {
							return err
						}
					}
				}
				for _, a := range adhoc {
					if err := tx.Create(&models.OrderItem{
						OrderUUID: existing.ID, ProductName: a.name,
						Quantity: a.qty, UnitPrice: a.price,
					}).Error; err != nil {
						return err
					}
				}
				// Recomputar Total desde las líneas reales (council BUG-PRICE-DRIFT):
				// nunca dejar Total != suma(unit_price*quantity).
				var fresh models.OrderTicket
				if err := tx.Preload("Items").First(&fresh, "id = ?", existing.ID).Error; err != nil {
					return err
				}
				if err := tx.Model(&fresh).Update("total", sumItems(fresh.Items)).Error; err != nil {
					return err
				}
				tx.Preload("Items").First(&fresh, "id = ?", existing.ID)
				result = fresh
				return nil
			})
			if txErr == nil || !isRetryableConflict(txErr) {
				break
			}
		}

		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no se pudo registrar el pedido en la mesa",
				"detail": txErr.Error(),
			})
			return
		}

		// Notificación in-app al tendero (la tarea de "cuenta por cobrar" ya
		// aparece sola en el Centro de Tareas vía tableAccountTasks).
		who := "Un cliente"
		if strings.EqualFold(strings.TrimSpace(req.PlacedBy), "mesero") {
			who = "El mesero"
		}
		CreateNotification(db, tenant.ID, "Nuevo pedido en mesa",
			fmt.Sprintf("%s pidió en %s", who, label), "table_order")

		c.JSON(http.StatusCreated, gin.H{
			"data": gin.H{
				"order_id":      result.ID,
				"session_token": result.SessionToken,
				"label":         result.Label,
				"status":        result.Status,
				"total":         result.Total,
			},
		})
	}
}
