// Spec: specs/038-push-notifications-web-android/spec.md
package push

import (
	"context"
	"fmt"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// CheckStockLow consulta el stock actual de los productos dados,
// compara contra el umbral del tenant, y dispara push "Stock bajo" por
// CADA producto que está en estado crítico (≤ umbral). El `dedup_key`
// `stock-low:<product_id>:<YYYY-MM-DD>` garantiza que el mismo producto
// no genere más de UNA push por día — incluso si el stock oscila o si
// se vende varias veces en el mismo día (AC-08).
//
// Si `dispatcher` es nil o `productIDs` está vacío, la función es
// un no-op silencioso. No retorna error — un fallo de push nunca debe
// abortar la venta del tendero (Art. II offline-first).
//
// Llamar SIEMPRE después de commitar la transacción de la venta. El
// SELECT lee el stock ya decrementado.
func CheckStockLow(ctx context.Context, db *gorm.DB, dispatcher *Dispatcher, tenantID string, productIDs []string) {
	if dispatcher == nil || len(productIDs) == 0 {
		return
	}

	threshold := loadStockThreshold(db, tenantID)
	if threshold <= 0 {
		// Tenant deshabilitó el chequeo (threshold ≤ 0) — respetar.
		return
	}

	// Cargar productos en stock crítico, scopeados por tenant_id.
	var products []models.Product
	if err := db.
		Where("tenant_id = ? AND id IN ? AND stock <= ? AND stock >= 0",
			tenantID, productIDs, threshold).
		Find(&products).Error; err != nil {
		// Best-effort: si el SELECT falla, simplemente no enviamos.
		return
	}

	today := time.Now().UTC().Format("2006-01-02")
	for _, p := range products {
		_, _ = dispatcher.DispatchEvent(ctx, db, Event{
			TenantID: tenantID,
			Type:     "stock_low",
			Title:    "Stock bajo: " + p.Name,
			Body:     fmt.Sprintf("Quedan %d unidades. Conviene reponer pronto.", p.Stock),
			DeepLink: "/inventario/" + p.ID,
			DedupKey: "stock-low:" + p.ID + ":" + today,
		})
	}
}

// loadStockThreshold lee el umbral configurado por el tenant; si no
// está configurado (NULL), retorna el default StockLowThresholdDefault.
// Si el query falla, retorna 0 (deshabilita el chequeo) — fail-closed
// para no enviar push espurias.
func loadStockThreshold(db *gorm.DB, tenantID string) int {
	var tenant models.Tenant
	if err := db.Select("stock_low_threshold").
		Where("id = ?", tenantID).
		First(&tenant).Error; err != nil {
		return 0
	}
	if tenant.StockLowThreshold == nil {
		return models.StockLowThresholdDefault
	}
	if *tenant.StockLowThreshold <= 0 {
		// Valor explícito ≤ 0 = tendero deshabilitó el chequeo.
		return 0
	}
	return *tenant.StockLowThreshold
}
