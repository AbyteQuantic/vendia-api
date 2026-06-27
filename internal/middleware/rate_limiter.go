package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type visitor struct {
	count    int
	windowAt time.Time
}

type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    int
	window   time.Duration
}

func NewRateLimiter(limit int, window time.Duration) gin.HandlerFunc {
	rl := &rateLimiter{
		visitors: make(map[string]*visitor),
		limit:    limit,
		window:   window,
	}

	go rl.cleanup()

	return rl.handler
}

// rateKey decide el cubo del limitador. CRÍTICO para móvil/5G: bajo CGNAT
// (carrier-grade NAT) miles de usuarios comparten UNA IP pública, así que limitar
// por IP castiga a usuarios legítimos con tráfico ajeno (429 espurios). Cuando hay
// sesión (header Authorization), limitamos por TOKEN — aísla cada dispositivo/
// cuenta. Las rutas anónimas (login, catálogo público) siguen por IP, que es lo
// correcto para frenar abuso desde una sola fuente.
func rateKey(c *gin.Context) string {
	if auth := c.GetHeader("Authorization"); auth != "" {
		return "tok:" + auth
	}
	return "ip:" + c.ClientIP()
}

func (rl *rateLimiter) handler(c *gin.Context) {
	key := rateKey(c)

	rl.mu.Lock()
	v, exists := rl.visitors[key]
	now := time.Now()
	if !exists || now.Sub(v.windowAt) > rl.window {
		rl.visitors[key] = &visitor{count: 1, windowAt: now}
		rl.mu.Unlock()
		c.Next()
		return
	}

	v.count++
	if v.count > rl.limit {
		rl.mu.Unlock()
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": "demasiadas solicitudes, intenta más tarde",
		})
		return
	}
	rl.mu.Unlock()
	c.Next()
}

func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for key, v := range rl.visitors {
			if now.Sub(v.windowAt) > rl.window*2 {
				delete(rl.visitors, key)
			}
		}
		rl.mu.Unlock()
	}
}
