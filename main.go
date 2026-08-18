package main

import (
	"goapi/config"
	"goapi/handlers"
	"goapi/middleware"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system env")
	}

	// Connect & migrate DB
	config.InitLogger() // Add this line
	config.ConnectDB()
	config.MigrateDB()
	handlers.InitFCM() // Add this line
	handlers.InitEmailService()

	// Initialize Firebase
	if err := handlers.InitFirebase(); err != nil {
		log.Printf("Warning: Firebase not initialized: %v", err)
	}

	// Router
	r := gin.Default()

	// CORS middleware (for your frontend)
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// Public routes (no auth)
	api := r.Group("/v1")
	api.Use(middleware.RateLimit(30, time.Minute))
	{
		// Rider auth endpoints
		api.POST("/riders/check-email", handlers.CheckEmail)
		api.POST("/riders/verify-otp", handlers.VerifyOTP)
		api.POST("/riders/google-login", handlers.GoogleLogin)
	}

	// Protected routes (require auth)
	protected := r.Group("/v1")
	protected.Use(middleware.RateLimit(30, time.Minute))
	protected.Use(middleware.RiderAuthMiddleware()) // Apply auth middleware
	{
		protected.GET("/get-driver", handlers.GetDriver)
		protected.POST("/book", handlers.BookRide)
		protected.POST("/cancel-booking", handlers.CancelBooking)
		protected.GET("/driver/active", handlers.GetActiveAssignments)
		// protected.GET("/rides/history", handlers.GetRiderHistory) // New endpoint
	}

	// Public estimate (rate limited separately)
	estimate := r.Group("/v1")
	estimate.Use(middleware.RateLimit(10, time.Minute))
	estimate.POST("/estimate", handlers.GetEstimate)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	log.Println("Go API running on :" + port)
	r.Run(":" + port)
}
