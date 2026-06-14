// Spec: specs/047-offline-sync-contract/spec.md
package services

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// El bug bloqueante: el cliente mandaba la PK bajo `uuid`, pero SyncOperation.ID
// tiene `json:"id" binding:"required"`. Aquí ejercitamos el camino REAL de bind
// (ShouldBindJSON) que ninguna prueba en memoria cubría.
func TestSyncEnvelopeBindsID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	bind := func(body string) (SyncRequest, error) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		var sr SyncRequest
		err := c.ShouldBindJSON(&sr)
		return sr, err
	}

	t.Run("envelope con id válido bindea y puebla ID", func(t *testing.T) {
		sr, err := bind(`{"operations":[{"id":"op-1","entity":"customer",` +
			`"action":"create","data":{"name":"Rosa"},` +
			`"client_updated_at":"2026-06-14T00:00:00Z"}]}`)
		if err != nil {
			t.Fatalf("bind falló con id válido: %v", err)
		}
		if len(sr.Operations) != 1 || sr.Operations[0].ID != "op-1" {
			t.Fatalf("ID no se pobló: %+v", sr.Operations)
		}
		if sr.Operations[0].Data["name"] != "Rosa" {
			t.Fatalf("data no es objeto: %+v", sr.Operations[0].Data)
		}
	})

	t.Run("la llave vieja 'uuid' deja ID vacío (la PK no se puebla)", func(t *testing.T) {
		// Gin no valida binding:"required" en elementos de slice sin `dive`,
		// así que el bind NO falla; pero ID queda "" → el insert escribe una
		// PK vacía y rompe en la DB. Por eso el cliente debe mandar `id`.
		sr, err := bind(`{"operations":[{"uuid":"op-1","entity":"customer",` +
			`"action":"create","data":{},` +
			`"client_updated_at":"2026-06-14T00:00:00Z"}]}`)
		if err != nil {
			t.Fatalf("bind inesperadamente falló: %v", err)
		}
		if sr.Operations[0].ID != "" {
			t.Fatalf("se esperaba ID vacío con la llave vieja, got %q", sr.Operations[0].ID)
		}
	})

	t.Run("data como String (bug viejo) es rechazado por el bind", func(t *testing.T) {
		_, err := bind(`{"operations":[{"id":"op-1","entity":"customer",` +
			`"action":"create","data":"{\"name\":\"Rosa\"}",` +
			`"client_updated_at":"2026-06-14T00:00:00Z"}]}`)
		if err == nil {
			t.Fatal("se esperaba error: data String no entra en map[string]any")
		}
	})
}
