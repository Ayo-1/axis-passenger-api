package main

import (
	"goapi/config"
	"goapi/handlers"
	"goapi/handlers/web"
	"goapi/middleware"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"goapi/services"
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
	config.InitRedis()
	handlers.InitFCM() // Add this line
	handlers.InitEmailService()

	paystackSvc := services.NewPaystackService(os.Getenv("PAYSTACK_SECRET_KEY_TEST"))
	stripeSvc := services.NewStripeService(os.Getenv("STRIPE_SECRET_KEY"))

	web.InitPaymentServices(paystackSvc, stripeSvc)

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
		api.POST("/riders/apple-login", handlers.AppleLogin)
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




	// Web routes
	webRoutes := r.Group("/v1/app")
	webRoutes.Use(middleware.RateLimit(30, time.Minute))
	{
		webRoutes.GET("/config", web.GetAppConfig)
		webRoutes.POST("/bookings/verify-payment", web.VerifyBookingPayment)
		webRoutes.POST("/bookings/estimate", web.CreateEstimate)
		webRoutes.POST("/bookings/driver/change", web.ChangeDriver)
		webRoutes.POST("/bookings", web.CreateBooking)
		webRoutes.POST("/bookings/lookup", web.LookupBooking)
		webRoutes.PATCH("/bookings/update", web.UpdateBooking)
		webRoutes.POST("/bookings/cancel", web.CancelBooking)

		webRoutes.POST("/rentals", web.CreateRental)

	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	log.Println("Go API running on :" + port)
	r.Run(":" + port)
}
