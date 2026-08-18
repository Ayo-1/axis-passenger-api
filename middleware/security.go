package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type client struct {
	count    int
	lastSeen time.Time
}

var (
	mu      sync.Mutex
	clients = make(map[string]*client)
)

func init() {
	go func() {
		for {
			time.Sleep(time.Minute)
			mu.Lock()
			for ip, c := range clients {
				if time.Since(c.lastSeen) > 2*time.Minute {
					delete(clients, ip)
				}
			}
			mu.Unlock()
		}
	}()
}

func RateLimit(maxRequests int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		mu.Lock()
		cl, exists := clients[ip]
		if !exists || time.Since(cl.lastSeen) > window {
			clients[ip] = &client{count: 1, lastSeen: time.Now()}
			mu.Unlock()
			c.Next()
			return
		}
		cl.count++
		count := cl.count
		mu.Unlock()

		if count > maxRequests {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Next()
	}
}