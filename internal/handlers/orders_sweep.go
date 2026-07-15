// Spec: specs/105-hito-restaurante-comandas/spec.md — F2.
//
// Sweep de tickets HUÉRFANOS del KDS: un pedido PREPAGO que lleva más de
// `maxAge` en 'listo' casi seguro ya se entregó y nadie tocó el botón —
// auto-entregarlo lo saca de la franja "Listos" de todos los dispositivos.
//
// Regla de oro: SOLO tickets con paid_at (mostrador prepago, la venta ya
// existe). Una cuenta de mesa sin pagar JAMÁS se toca automáticamente:
// cerrarla u ocultarla haría desaparecer dinero pendiente (Art. VII).
package handlers

import (
	"net/http"
	"time"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SweepOrphanOrders marca 'entregado' los tickets prepago varados en 'listo'
// hace más de maxAge. Devuelve cuántos tocó. Idempotente: la segunda pasada
// no encuentra nada.
func SweepOrphanOrders(db *gorm.DB, maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge)
	now := time.Now()
	res := db.Model(&models.OrderTicket{}).
		Where("status = ? AND paid_at IS NOT NULL AND listo_at IS NOT NULL AND listo_at < ?",
			models.OrderStatusListo, cutoff).
		Updates(map[string]any{
			"status":       models.OrderStatusEntregado,
			"entregado_at": now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// OrdersSweepJob — endpoint interno (Bearer CRON_TOKEN, igual que el resto
// de /internal/jobs/*) que corre el sweep cada ~15 min desde cron-jobs.yml.
func OrdersSweepJob(db *gorm.DB) gin.HandlerFunc {
	const orphanAge = 45 * time.Minute
	return func(c *gin.Context) {
		if !cronAuthOK(c) {
			return
		}
		n, err := SweepOrphanOrders(db, orphanAge)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "sweep de comandas falló"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"auto_entregados": n}})
	}
}
