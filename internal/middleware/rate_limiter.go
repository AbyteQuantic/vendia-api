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

func (rl *rateLimiter) handler(c *gin.Context) {
	ip := c.ClientIP()

	rl.mu.Lock()
	v, exists := rl.visitors[ip]
	now := time.Now()
	if !exists || now.Sub(v.windowAt) > rl.window {
		rl.visitors[ip] = &visitor{count: 1, windowAt: now}
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
		for ip, v := range rl.visitors {
			if now.Sub(v.windowAt) > rl.window*2 {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}
