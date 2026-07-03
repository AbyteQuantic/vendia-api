// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// rankCache — orden del re-rank por IA cacheado por (tenant + hora + conjunto de
// ids), TTL 5 min, para no llamar a Gemini en cada poll de 15s. Spec 078 F3.
type rankEntry struct {
	order  []string
	expiry time.Time
}

var rankCache sync.Map // string -> rankEntry

func rankCacheKey(tenantID string, hour int, tasks []models.Task) string {
	ids := services.TaskIDs(tasks)
	sort.Strings(ids)
	return tenantID + "|" + itoaHour(hour) + "|" + strings.Join(ids, ",")
}

func itoaHour(h int) string { return fmt.Sprintf("%d", h) }

// rankTasks aplica el re-rank por IA con caché. Sin IA o sin caché válido, llama
// a RankTasks (que internamente cae al orden por reglas si la IA falla).
func rankTasks(tenantID string, tasks []models.Task, gen services.GenerateFunc) []models.Task {
	if len(tasks) < 2 || gen == nil {
		return tasks
	}
	hour := time.Now().Hour()
	key := rankCacheKey(tenantID, hour, tasks)
	if v, ok := rankCache.Load(key); ok {
		if e := v.(rankEntry); time.Now().Before(e.expiry) {
			if reordered := services.ApplyTaskOrder(tasks, e.order); reordered != nil {
				return reordered
			}
		}
	}
	reordered := services.RankTasks(tasks, hour, gen)
	rankCache.Store(key, rankEntry{order: services.TaskIDs(reordered), expiry: time.Now().Add(5 * time.Minute)})
	return reordered
}

// ListTasks — GET /api/v1/tasks  (Spec 078, Fase 0)
// Agregador read-only: DERIVA las tareas pendientes del tenant leyendo las
// entidades dueñas (pedidos online, mesas/órdenes, mandados, productos), sin una
// tabla 'tasks'. Deduplicado por id "{kind}:{source}", filtrado por descartes
// vigentes, ordenado por urgencia (lo más urgente y lo que más espera, arriba).
func ListTasks(db *gorm.DB, gen services.GenerateFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		// Sede activa SOLO para las fuentes FÍSICAS (mesas/stock/perecederos), que
		// deben coincidir con la sede de su pantalla. Pedidos web, mandados y
		// recetas son a NIVEL DE NEGOCIO (tenant-wide, visibles desde cualquier
		// sede) — antes un pedido de otra sede salía como tarea pero la pantalla
		// no lo mostraba (Spec 078, fix consistencia tarea↔pantalla).
		branchID := ResolveBranchScope(c, db).BranchID

		tasks := make([]models.Task, 0, 16)
		tasks = append(tasks, onlineOrderTasks(db, tenantID)...)            // negocio
		tasks = append(tasks, tableAccountTasks(db, tenantID, branchID)...) // sede activa
		tasks = append(tasks, errandTasks(db, tenantID)...)                 // negocio
		if t, ok := outOfStockTask(db, tenantID, branchID); ok {            // sede activa
			tasks = append(tasks, t)
		}
		if t, ok := reorderTask(db, tenantID, branchID); ok { // sede activa
			tasks = append(tasks, t)
		}
		if t, ok := perishableTask(db, tenantID, branchID); ok { // sede activa
			tasks = append(tasks, t)
		}
		if t, ok := incompleteMenuTask(db, tenantID); ok { // recetas globales
			tasks = append(tasks, t)
		}

		tasks = filterDismissed(db, tenantID, tasks)
		tasks = dedupeAndSort(tasks)
		// Re-rank por IA SEGÚN CONTEXTO (no bloqueante; cae al orden por reglas).
		tasks = rankTasks(tenantID, tasks, gen)

		urgent, important := 0, 0
		for _, t := range tasks {
			switch t.Urgency {
			case models.TaskUrgencyCritical, models.TaskUrgencyHigh:
				urgent++
			case models.TaskUrgencyNormal:
				important++
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"tasks":  tasks,
			"counts": gin.H{"urgent": urgent, "important": important, "actionable": urgent + important, "total": len(tasks)},
		}})
	}
}

// scopeBranch aplica el filtro multi-sede: la sede pedida MÁS los globales (NULL).
func scopeBranch(q *gorm.DB, branchID string) *gorm.DB {
	if branchID != "" {
		return q.Where("branch_id = ? OR branch_id IS NULL", branchID)
	}
	return q
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "hace un momento"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("hace %d días", int(d.Hours()/24))
	}
}

// T1/T2 — pedidos en línea por aceptar (pending) o por entregar (accepted).
// Tenant-wide: un pedido web es a nivel de negocio, visible desde cualquier sede.
func onlineOrderTasks(db *gorm.DB, tenantID string) []models.Task {
	var rows []models.OnlineOrder
	q := db.Where("tenant_id = ? AND status IN ?", tenantID, []string{"pending", "accepted"})
	q.Order("created_at ASC").Limit(50).Find(&rows)
	out := make([]models.Task, 0, len(rows))
	for _, o := range rows {
		who := o.CustomerName
		if who == "" {
			who = "un cliente"
		}
		// Spec 083 — pedido de mesa: el destino es la MESA, no un cliente. El
		// personal necesita ver claro a qué mesa va y cuánto lleva esperando
		// (recordatorio de tiempo de solicitud / demora de entrega).
		isMesa := o.DeliveryType == "mesa" || o.TableLabel != ""
		t := models.Task{
			ID: models.TaskOnlineOrder + ":" + o.ID, Kind: models.TaskOnlineOrder, SourceID: o.ID,
			DeepLink: "/online-orders/" + o.ID, Amount: o.TotalAmount, CreatedAt: o.CreatedAt,
		}
		if o.Status == "pending" {
			if isMesa {
				t.Title = "Pedido · " + o.TableLabel
				t.Subtitle = ago(o.CreatedAt) + " · por aceptar"
			} else {
				t.Title = "Pedido en línea de " + who
				t.Subtitle = ago(o.CreatedAt) + " · por aceptar"
			}
			t.Urgency = models.TaskUrgencyCritical
			t.ActionLabel = "Aceptar"
		} else {
			if isMesa {
				t.Title = "Entregar a " + o.TableLabel
			} else {
				t.Title = "Entregar pedido de " + who
			}
			// Recordatorio de demora: mostramos cuánto lleva "aceptado · por
			// entregar" para que el personal no lo olvide en la mesa.
			t.Subtitle = ago(o.CreatedAt) + " · por entregar"
			t.Urgency = models.TaskUrgencyHigh
			t.ActionLabel = "Marcar entregado"
		}
		out = append(out, t)
	}
	return out
}

// T3 — mesas/cuentas abiertas por cobrar (nuevo/preparando/listo).
func tableAccountTasks(db *gorm.DB, tenantID, branchID string) []models.Task {
	var rows []models.OrderTicket
	q := scopeBranch(db.Where("tenant_id = ? AND status IN ?", tenantID,
		[]string{string(models.OrderStatusNuevo), string(models.OrderStatusPreparando), string(models.OrderStatusListo)}), branchID)
	q.Order("created_at ASC").Limit(50).Find(&rows)
	out := make([]models.Task, 0, len(rows))
	for _, o := range rows {
		label := o.Label
		if label == "" {
			label = "Cuenta"
		}
		t := models.Task{
			ID: models.TaskTableAccount + ":" + o.ID, Kind: models.TaskTableAccount, SourceID: o.ID,
			Title: label, DeepLink: "/orders/" + o.ID, Amount: o.Total, CreatedAt: o.CreatedAt,
			ActionLabel: "Cobrar", SessionToken: o.SessionToken,
		}
		if o.Status == models.OrderStatusListo {
			t.Subtitle = "Lista para cobrar"
			t.Urgency = models.TaskUrgencyCritical
		} else {
			t.Subtitle = "En preparación · cuenta abierta"
			t.Urgency = models.TaskUrgencyHigh
		}
		out = append(out, t)
	}
	return out
}

// T6 — mandados/compras pendientes (pendiente/enviado).
func errandTasks(db *gorm.DB, tenantID string) []models.Task {
	var rows []models.PurchaseErrand
	db.Where("tenant_id = ? AND status IN ?", tenantID, []string{"pendiente", "enviado"}).
		Order("created_at ASC").Limit(50).Find(&rows)
	out := make([]models.Task, 0, len(rows))
	for _, e := range rows {
		title := e.Title
		if title == "" {
			title = "Compra de insumos"
		}
		sub := "Pendiente de compra"
		if e.AssigneeName != "" {
			sub = "Asignado a " + e.AssigneeName
		}
		out = append(out, models.Task{
			ID: models.TaskErrand + ":" + e.ID, Kind: models.TaskErrand, SourceID: e.ID,
			Title: title, Subtitle: sub, Urgency: models.TaskUrgencyNormal,
			ActionLabel: "Ver mandado", DeepLink: "/mandados", CreatedAt: e.CreatedAt,
		})
	}
	return out
}

// T7 — stock bajo (agregada), partida por severidad: un producto AGOTADO
// (stock<=0) es más urgente que uno bajo su mínimo pero disponible. Antes una
// sola tarjeta "normal" nunca ganaba el slot del toast (Spec 078 F3) frente al
// toast legacy de stock bajo por producto (push.CheckStockLow) — ahora el
// agotado sube a "high" y sí lo desplaza.
func reorderTask(db *gorm.DB, tenantID, branchID string) (models.Task, bool) {
	var n int64
	q := scopeBranch(db.Model(&models.Product{}).
		Where("tenant_id = ? AND min_stock > 0 AND stock > 0 AND stock <= min_stock AND is_available = ?", tenantID, true), branchID)
	q.Count(&n)
	if n == 0 {
		return models.Task{}, false
	}
	return models.Task{
		ID: models.TaskReorder + ":" + tenantID, Kind: models.TaskReorder, SourceID: tenantID,
		Title: "Productos por reordenar", Subtitle: fmt.Sprintf("%d en su mínimo o por debajo", n),
		Urgency: models.TaskUrgencyNormal, Count: int(n), ActionLabel: "Reordenar",
		DeepLink: "/inventory/reorder", CreatedAt: time.Now(),
	}, true
}

// T7b — productos AGOTADOS (agregada): stock<=0 con reposición configurada.
// Urgencia high para que gane el slot del toast (Spec 078 F3).
func outOfStockTask(db *gorm.DB, tenantID, branchID string) (models.Task, bool) {
	var n int64
	q := scopeBranch(db.Model(&models.Product{}).
		Where("tenant_id = ? AND min_stock > 0 AND stock <= 0 AND is_available = ?", tenantID, true), branchID)
	q.Count(&n)
	if n == 0 {
		return models.Task{}, false
	}
	return models.Task{
		ID: models.TaskReorderOut + ":" + tenantID, Kind: models.TaskReorderOut, SourceID: tenantID,
		Title: "Productos agotados", Subtitle: fmt.Sprintf("%d sin unidades disponibles", n),
		Urgency: models.TaskUrgencyHigh, Count: int(n), ActionLabel: "Reordenar",
		DeepLink: "/inventory/reorder", CreatedAt: time.Now(),
	}, true
}

// T8 — perecederos por vencer (agregada): N productos vencen en ≤7 días.
func perishableTask(db *gorm.DB, tenantID, branchID string) (models.Task, bool) {
	today := time.Now().Format("2006-01-02")
	limit := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	var n int64
	q := scopeBranch(db.Model(&models.Product{}).
		Where("tenant_id = ? AND expiry_date IS NOT NULL AND expiry_date >= ? AND expiry_date <= ? AND stock > 0", tenantID, today, limit), branchID)
	q.Count(&n)
	if n == 0 {
		return models.Task{}, false
	}
	return models.Task{
		ID: models.TaskPerishable + ":" + tenantID, Kind: models.TaskPerishable, SourceID: tenantID,
		Title: "Productos por vencer", Subtitle: fmt.Sprintf("%d vencen esta semana", n),
		Urgency: models.TaskUrgencyNormal, Count: int(n), ActionLabel: "Crear promoción",
		DeepLink: "/promotions/suggestions", CreatedAt: time.Now(),
	}, true
}

// T13 — platos de menú INCOMPLETOS (agregada): importados/creados sin una receta
// con ingredientes → no se pueden costear. Tarea persistente para completarlos.
// DERIVADO (sin flag): is_menu_item sin una receta que tenga ingredientes.
func incompleteMenuTask(db *gorm.DB, tenantID string) (models.Task, bool) {
	var completeIDs []string
	db.Table("recipe_ingredients ri").
		Joins("JOIN recipes r ON r.id = ri.recipe_uuid").
		// ri.deleted_at IS NULL: el Table crudo no aplica el scope soft-delete de
		// GORM. Sin esto, un insumo borrado seguiría contando el plato como
		// "completo" y desaparecería erróneamente de "Complete sus recetas".
		Where("r.tenant_id = ? AND r.product_id IS NOT NULL AND r.deleted_at IS NULL AND ri.deleted_at IS NULL", tenantID).
		Distinct().Pluck("r.product_id", &completeIDs)

	var n int64
	// Tenant-wide: los platos de menú son globales (branch NULL).
	q := db.Model(&models.Product{}).
		Where("tenant_id = ? AND is_menu_item = ?", tenantID, true)
	if len(completeIDs) > 0 {
		q = q.Where("id NOT IN ?", completeIDs)
	}
	q.Count(&n)
	if n == 0 {
		return models.Task{}, false
	}
	return models.Task{
		ID: models.TaskMenuIncomplete + ":" + tenantID, Kind: models.TaskMenuIncomplete, SourceID: tenantID,
		Title: "Complete sus recetas", Subtitle: fmt.Sprintf("%d plato(s) sin ingredientes ni costo", n),
		Urgency: models.TaskUrgencyNormal, Count: int(n), ActionLabel: "Completar",
		DeepLink: "/recipes", CreatedAt: time.Now(),
	}, true
}

// filterDismissed quita tareas pospuestas cuyo plazo aún no vence.
func filterDismissed(db *gorm.DB, tenantID string, tasks []models.Task) []models.Task {
	var dis []models.TaskDismissal
	db.Where("tenant_id = ? AND dismissed_until > ?", tenantID, time.Now()).Find(&dis)
	if len(dis) == 0 {
		return tasks
	}
	blocked := make(map[string]bool, len(dis))
	for _, d := range dis {
		blocked[d.TaskID] = true
	}
	out := tasks[:0]
	for _, t := range tasks {
		if !blocked[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// dedupeAndSort: una entidad = una tarjeta; orden por urgencia y luego antigüedad.
func dedupeAndSort(tasks []models.Task) []models.Task {
	seen := make(map[string]bool, len(tasks))
	uniq := make([]models.Task, 0, len(tasks))
	for _, t := range tasks {
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		uniq = append(uniq, t)
	}
	sort.SliceStable(uniq, func(a, b int) bool {
		ra, rb := models.TaskUrgencyRank(uniq[a].Urgency), models.TaskUrgencyRank(uniq[b].Urgency)
		if ra != rb {
			return ra > rb // más urgente primero
		}
		return uniq[a].CreatedAt.Before(uniq[b].CreatedAt) // lo que más espera, arriba
	})
	return uniq
}

// DismissTask — POST /api/v1/tasks/dismiss  {task_id, hours?} (default 24h).
// Pospone una tarea AGREGADA (reorder/perishable). Spec 078.
func DismissTask(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req struct {
			TaskID string `json:"task_id"`
			Hours  int    `json:"hours"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.TaskID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "task_id requerido"})
			return
		}
		hours := req.Hours
		if hours <= 0 {
			hours = 24
		}
		d := models.TaskDismissal{
			ID:             tenantID + ":" + req.TaskID,
			TenantID:       tenantID,
			TaskID:         req.TaskID,
			DismissedUntil: time.Now().Add(time.Duration(hours) * time.Hour),
			CreatedAt:      time.Now(),
		}
		// upsert por PK (re-posponer renueva el plazo).
		if err := db.Save(&d).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo posponer la tarea"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"dismissed_until": d.DismissedUntil}})
	}
}
