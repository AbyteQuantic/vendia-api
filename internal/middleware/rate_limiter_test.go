// Spec: specs/025-captcha-pedidos-publicos/spec.md
package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"vendia-backend/internal/middleware"
)

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.NewRateLimiter(3, 1*time.Minute))
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "request %d should succeed", i+1)
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.NewRateLimiter(2, 1*time.Minute))
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

// TestOrderRateLimiter_Cinco verifica que un rate-limiter configurado a
// 5 / 15 min / IP (el dedicado de F025 para rutas de pedido público)
// deja pasar los primeros 5 requests del mismo IP y rechaza el 6º con
// 429. (AC-04, T-04)
func TestOrderRateLimiter_Cinco(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Configurar el mismo rate-limiter dedicado que se aplica en main.go
	// a las rutas de pedido público: 5 req / 15 min / IP.
	r.Use(middleware.NewRateLimiter(5, 15*time.Minute))
	r.POST("/order", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// Los primeros 5 deben pasar.
	for i := 1; i <= 5; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/order", nil)
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "request %d debería pasar", i)
	}

	// El 6º debe ser rechazado con 429.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/order", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code, "el 6º request debe recibir 429")
	assert.Contains(t, w.Body.String(), "demasiadas solicitudes")
}

// TestOrderRateLimiter_VentanaIndependiente verifica que dos instancias
// distintas de NewRateLimiter no comparten estado — un limiter de 5/15min
// para pedidos no afecta al limiter global de 100/min. (D4, FR-02)
func TestOrderRateLimiter_VentanaIndependiente(t *testing.T) {
	gin.SetMode(gin.TestMode)

	orderLimiter := middleware.NewRateLimiter(5, 15*time.Minute)
	globalLimiter := middleware.NewRateLimiter(100, 1*time.Minute)

	rOrder := gin.New()
	rOrder.Use(orderLimiter)
	rOrder.POST("/orders", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	rGlobal := gin.New()
	rGlobal.Use(globalLimiter)
	rGlobal.GET("/products", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// Agotar el orderLimiter (5 requests).
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/orders", nil)
		rOrder.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}
	// 6º pedido → 429.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/orders", nil)
	rOrder.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	// El globalLimiter sigue en 0 — no debe verse afectado.
	wg := httptest.NewRecorder()
	reqG, _ := http.NewRequest(http.MethodGet, "/products", nil)
	rGlobal.ServeHTTP(wg, reqG)
	assert.Equal(t, http.StatusOK, wg.Code, "el limiter global no debe estar afectado")
}
