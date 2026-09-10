package main

import (
	"goapi/config"
	"goapi/handlers"
	"goapi/handlers/web"
	"goapi/middleware"
	"log"
	"log/slog"
	"os"
	"time"

	"goapi/services"

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
	config.InitRedis()
	handlers.InitFCM() // Add this line
	handlers.InitEmailService()
	web.InitEmailService()
	handlers.InitAdminSMS()

	paystackSvc := services.NewPaystackService(os.Getenv("PAYSTACK_SECRET_KEY"))
	stripeSvc := services.NewStripeService(os.Getenv("STRIPE_SECRET_KEY"))

	web.InitPaymentServices(paystackSvc, stripeSvc)

	web.StartPaymentReconciler()
	// Booking cleanup: expire dead intents, then stale pending bookings
	go func() {
		slog.Info("booking cleanup worker started")
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			config.DB.Exec(`
				UPDATE payment_intents SET status = 'expired', updated_at = NOW()
				WHERE status = 'pending' AND created_at < ?
			`, time.Now().Add(-24*time.Hour))

			res := config.DB.Exec(`
				UPDATE bookings b
				SET b.status = 'expired', b.updated_at = NOW()
				WHERE b.status = 'pending'
				  AND b.payment_status = 'pending'
				  AND b.created_at < ?
				  AND NOT EXISTS (
				    SELECT 1 FROM payment_intents pi
				    WHERE pi.reference = SUBSTRING_INDEX(SUBSTRING_INDEX(b.session_id, '-OUT', 1), '-RTN', 1)
				      AND pi.status = 'paid'
				  )
			`, time.Now().Add(-2*time.Hour))
			if res.Error != nil {
				slog.Error("expire stale bookings", "error", res.Error)
				continue
			}
			if res.RowsAffected > 0 {
				slog.Info("expired stale pending bookings", "count", res.RowsAffected)
			}
		}
	}()

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
		webRoutes.POST("/bookings/receipt", web.GetBookingReceipt)
		webRoutes.POST("/bookings/receipt/link", web.CreateReceiptLink)
		webRoutes.POST("/bookings/receipt/token", web.GetReceiptByToken)
		webRoutes.GET("/pay/details", web.GetGuestPaymentDetails)
		webRoutes.POST("/pay/create-link", web.CreateGuestPaymentLink)

		webRoutes.POST("/rentals", web.CreateRental)

	}

	hotelGroup := r.Group("/v1/app/hotels")

	// Public hotel routes
	publicHotel := hotelGroup.Group("/")
	publicHotel.Use(middleware.RateLimit(30, time.Minute))
	{
		publicHotel.POST("/apply", web.ApplyHotel)
		publicHotel.POST("/auth/login", web.LoginHotel)
		publicHotel.POST("/auth/google", web.GoogleLoginHotel)

	}
	// Protected hotel routes
	protectedHotel := hotelGroup.Group("/")
	protectedHotel.Use(
		middleware.HotelAuthRequired(),
		middleware.RateLimit(30, time.Minute),
	)
	{
		protectedHotel.GET("/me", web.HotelMe)
		protectedHotel.POST("/setup", web.SetupHotel)
		protectedHotel.GET("/dashboard", web.HotelDashboard)
		protectedHotel.POST("/bookings", web.BookForGuest)
		protectedHotel.GET("/bookings", web.ListHotelBookings)
		protectedHotel.GET("/earnings", web.HotelEarnings)
		protectedHotel.POST("/withdraw", web.RequestPayout)
		protectedHotel.POST("/bookings/status", web.UpdatePartnerBookingStatus)
		protectedHotel.POST("/rentals", web.BookRentalForGuest)

	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	log.Println("Go API running on :" + port)
	r.Run(":" + port)
}
