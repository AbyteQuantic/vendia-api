// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type errandLineReq struct {
	IngredientID string  `json:"ingredient_id"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Qty          float64 `json:"qty"`
	UnitPrice    float64 `json:"unit_price"`
	Cost         float64 `json:"cost"`
	PriceSource  string  `json:"price_source"`
	IsEstimate   bool    `json:"is_estimate"`
}

// CreateErrand — POST /api/v1/errands
// Crea un MANDADO de compra (lista + a quién se asigna) y, si hay teléfono,
// devuelve el link de WhatsApp para enviarlo. Registra la INTENCIÓN → permite
// luego "reenviar lo de hoy". VendIA solo conecta (no procesa pago).
func CreateErrand(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req struct {
			Title         string          `json:"title"`
			AssigneeType  string          `json:"assignee_type"`
			AssigneeID    string          `json:"assignee_id"`
			AssigneeName  string          `json:"assignee_name"`
			AssigneePhone string          `json:"assignee_phone"`
			Note          string          `json:"note"`
			BranchID      string          `json:"branch_id"`
			Lines         []errandLineReq `json:"lines"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Lines) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "El mandado no tiene productos."})
			return
		}
		at := req.AssigneeType
		switch at {
		case models.AssigneeSupplier, models.AssigneeWhatsAppContact, models.AssigneeEmployee, models.AssigneeSelf:
		default:
			at = models.AssigneeSelf
		}

		var total float64
		lines := make([]models.PurchaseErrandLine, 0, len(req.Lines))
		for _, l := range req.Lines {
			total += l.Cost
			var ingPtr *string
			if s := strings.TrimSpace(l.IngredientID); s != "" {
				ingPtr = &s
			}
			lines = append(lines, models.PurchaseErrandLine{
				IngredientID: ingPtr, Name: l.Name, Unit: l.Unit, Qty: l.Qty,
				EstimatedUnitPrice: l.UnitPrice, EstimatedCost: l.Cost,
				PriceSource: l.PriceSource, IsEstimate: l.IsEstimate,
			})
		}
		var assigneePtr *string
		if s := strings.TrimSpace(req.AssigneeID); s != "" {
			assigneePtr = &s
		}
		errand := models.PurchaseErrand{
			TenantID: tenantID, BranchID: req.BranchID, Title: req.Title,
			AssigneeType: at, AssigneeID: assigneePtr, AssigneeName: req.AssigneeName,
			AssigneePhone: req.AssigneePhone, Status: models.ErrandPendiente,
			TotalEstimated: total, Note: req.Note, Lines: lines,
		}
		if err := db.Create(&errand).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear el mandado"})
			return
		}

		waURL := ""
		if strings.TrimSpace(req.AssigneePhone) != "" {
			waSvc := services.NewWhatsAppService()
			waURL = waSvc.BuildURL(req.AssigneePhone, buildErrandMessage(errand))
		}
		c.JSON(http.StatusCreated, gin.H{"data": gin.H{"errand": errand, "whatsapp_url": waURL}})
	}
}

func buildErrandMessage(e models.PurchaseErrand) string {
	var b strings.Builder
	b.WriteString("Buenos días, necesito comprar:\n")
	for _, l := range e.Lines {
		qty := l.Qty
		b.WriteString("• " + l.Name)
		if qty > 0 {
			b.WriteString(" — " + trimFloat(qty) + " " + l.Unit)
		}
		b.WriteString("\n")
	}
	if e.TotalEstimated > 0 {
		b.WriteString("\nTotal aprox: $" + trimFloat(e.TotalEstimated))
	}
	return b.String()
}

// trimFloat — número sin ceros sobrantes ni notación científica (5.0→"5", 0.4→"0.4").
func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// ListErrands — GET /api/v1/errands[?status=]
func ListErrands(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		q := db.Preload("Lines").Where("tenant_id = ?", tenantID)
		if s := c.Query("status"); s != "" {
			q = q.Where("status = ?", s)
		}
		var errands []models.PurchaseErrand
		q.Order("created_at DESC").Limit(100).Find(&errands)
		c.JSON(http.StatusOK, gin.H{"data": errands})
	}
}

// UpdateErrandStatus — PATCH /api/v1/errands/:id
func UpdateErrandStatus(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req struct {
			Status string `json:"status"`
		}
		_ = c.ShouldBindJSON(&req)
		switch req.Status {
		case models.ErrandPendiente, models.ErrandEnviado, models.ErrandComprado, models.ErrandCancelado:
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "estado inválido"})
			return
		}
		updates := map[string]any{"status": req.Status}
		if req.Status == models.ErrandComprado || req.Status == models.ErrandCancelado {
			now := time.Now()
			updates["closed_at"] = &now
		}
		res := db.Model(&models.PurchaseErrand{}).
			Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).Updates(updates)
		if res.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "mandado no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "estado actualizado"})
	}
}

// MatchTodayErrand — POST /api/v1/errands/match-today {ingredient_ids:[...]}
// "Reenviar pedido del día": si HOY ya se creó un mandado con el MISMO conjunto
// de insumos, lo devuelve para que el tenant solo lo reenvíe.
func MatchTodayErrand(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req struct {
			IngredientIDs []string `json:"ingredient_ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.IngredientIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{"data": nil})
			return
		}
		want := normalizeIDSet(req.IngredientIDs)

		startOfDay := time.Now().Truncate(24 * time.Hour)
		var errands []models.PurchaseErrand
		db.Preload("Lines").
			Where("tenant_id = ? AND created_at >= ? AND status <> ?", tenantID, startOfDay, models.ErrandCancelado).
			Order("created_at DESC").Limit(20).Find(&errands)

		for _, e := range errands {
			got := make([]string, 0, len(e.Lines))
			for _, l := range e.Lines {
				if l.IngredientID != nil {
					got = append(got, *l.IngredientID)
				}
			}
			if sameSet(want, normalizeIDSet(got)) {
				c.JSON(http.StatusOK, gin.H{"data": e})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": nil})
	}
}

func normalizeIDSet(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
