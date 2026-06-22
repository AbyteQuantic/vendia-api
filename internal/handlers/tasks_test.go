// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func taskDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// OnlineOrder usa gen_random_uuid() (Postgres) → no AutoMigra en SQLite; su
	// fuente se cubre en el E2E de prod. Las otras 4 fuentes sí.
	require.NoError(t, db.AutoMigrate(&models.OrderTicket{},
		&models.PurchaseErrand{}, &models.Product{}, &models.TaskDismissal{}))
	return db
}

func tasksRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/tasks", handlers.ListTasks(db, nil))
	r.POST("/tasks/dismiss", handlers.DismissTask(db))
	return r
}

func decodeTasks(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var resp struct {
		Data struct {
			Tasks  []map[string]any `json:"tasks"`
			Counts map[string]any   `json:"counts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	return resp.Data.Tasks
}

func TestListTasks_AggregatesAndOrders(t *testing.T) {
	db := taskDB(t)
	// mesa lista (critical), mandado (normal), stock bajo (normal), perecedero (normal).
	require.NoError(t, db.Create(&models.OrderTicket{BaseModel: models.BaseModel{ID: "ot1"}, TenantID: "t1", Label: "Mesa 3", Status: models.OrderStatusListo, Total: 18000}).Error)
	require.NoError(t, db.Create(&models.PurchaseErrand{BaseModel: models.BaseModel{ID: "er1"}, TenantID: "t1", Status: "pendiente", Title: "Compra de insumos"}).Error)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", Name: "Arroz", Stock: 1, MinStock: 5, IsAvailable: true}).Error)
	exp := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p2"}, TenantID: "t1", Name: "Leche", Stock: 4, ExpiryDate: &exp, IsAvailable: true}).Error)

	w := doJSON(t, tasksRouter(db), http.MethodGet, "/tasks", nil)
	require.Equal(t, http.StatusOK, w.Code)
	tasks := decodeTasks(t, w.Body.Bytes())
	require.Len(t, tasks, 4)
	// La mesa lista (critical) va primero.
	assert.Equal(t, models.TaskUrgencyCritical, tasks[0]["urgency"])
	assert.Equal(t, "table_account:ot1", tasks[0]["id"])
	// ids son "{kind}:{source}".
	ids := map[string]bool{}
	for _, t := range tasks {
		ids[t["id"].(string)] = true
	}
	assert.True(t, ids["errand:er1"])
	assert.True(t, ids["reorder:t1"])
	assert.True(t, ids["perishable:t1"])
}

func TestListTasks_DismissHidesAggregated(t *testing.T) {
	db := taskDB(t)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", Name: "Arroz", Stock: 1, MinStock: 5, IsAvailable: true}).Error)
	r := tasksRouter(db)

	// Antes: aparece la tarea de reordenar.
	w1 := doJSON(t, r, http.MethodGet, "/tasks", nil)
	require.Len(t, decodeTasks(t, w1.Body.Bytes()), 1)

	// Posponer reorder:t1 → desaparece.
	w2 := doJSON(t, r, http.MethodPost, "/tasks/dismiss", map[string]any{"task_id": "reorder:t1", "hours": 24})
	require.Equal(t, http.StatusOK, w2.Code)
	w3 := doJSON(t, r, http.MethodGet, "/tasks", nil)
	assert.Empty(t, decodeTasks(t, w3.Body.Bytes()))
}
