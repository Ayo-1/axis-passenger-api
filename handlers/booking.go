// handlers/booking.go - Fixed version
package handlers

import (
	"fmt"
	"net/http"
	"os"
	"log"
	"strings"
	"time"

	"goapi/config"
	"goapi/models"

	"github.com/gin-gonic/gin"
)

type BookingRequest struct {
	SessionID      string  `json:"session_id" binding:"required"`
	DriverID       string  `json:"driver_id" binding:"required"`
	PickupAddress  string  `json:"pickup_address" binding:"required"`
	DropoffAddress string  `json:"dropoff_address" binding:"required"`
	PickupLat      float64 `json:"pickup_lat" binding:"required"`
	PickupLng      float64 `json:"pickup_long" binding:"required"`
	DropoffLat     float64 `json:"dropoff_lat" binding:"required"`
	DropoffLng     float64 `json:"dropoff_long" binding:"required"`
	DistanceKm     float64 `json:"distance_km" binding:"required"`
	DurationText   string  `json:"duration_text"`
	FareLow        float64 `json:"fare_low" binding:"required"`
	FareHigh       float64 `json:"fare_high" binding:"required"`
}

// Requires GOOGLE_MAPS_API_KEY env var (Google Static Maps API).
func buildMapURL(pickupLat, pickupLng, dropLat, dropLng float64) string {
	apiKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	return fmt.Sprintf(
		"https://maps.googleapis.com/maps/api/staticmap?size=600x300&maptype=roadmap"+
			"&markers=color:black|label:A|%f,%f"+
			"&markers=color:green|label:B|%f,%f"+
			"&path=color:0x111111|weight:3|%f,%f|%f,%f"+
			"&key=%s",
		pickupLat, pickupLng, dropLat, dropLng,
		pickupLat, pickupLng, dropLat, dropLng, apiKey,
	)
}


func BookRide(c *gin.Context) {
	var req BookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing required fields"})
		return
	}

	// Get rider ID from middleware with type assertion
	riderIDInterface, exists := c.Get("rider_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	riderID := riderIDInterface.(string) // Type assertion

	// Get rider email from middleware with type assertion
	riderEmailInterface, exists := c.Get("rider_email")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	riderEmail := riderEmailInterface.(string) // Type assertion

	req.DropoffAddress = strings.TrimSpace(req.DropoffAddress)

	if req.FareLow < 1 || req.FareHigh < req.FareLow || req.FareHigh > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid fare"})
		return
	}

	// Check assignment still active
	var assignment models.DriverAssignment
	if err := config.DB.Where("session_id = ? AND driver_id = ? AND expires_at > ?",
		req.SessionID, req.DriverID, time.Now()).First(&assignment).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Driver assignment expired, please get a new driver"})
		return
	}

	// No duplicate active bookings
	var existing models.Booking
	config.DB.Where("session_id = ? AND status IN ('pending','confirmed')", req.SessionID).First(&existing)
	if existing.ID != 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "You already have an active booking"})
		return
	}

	booking := models.Booking{
		SessionID:      req.SessionID,
		DriverID:       req.DriverID,
		PickupAddress:  req.PickupAddress,
		DropoffAddress: req.DropoffAddress,
		PickupLat:      req.PickupLat,
		PickupLng:      req.PickupLng,
		DropoffLat:     req.DropoffLat,
		DropoffLng:     req.DropoffLng,
		DistanceKm:     req.DistanceKm,
		DurationText:   req.DurationText,
		FareLow:        req.FareLow,
		FareHigh:       req.FareHigh,
		Status:         "confirmed",
		RiderID:        riderID,     // Now a string
		RiderEmail:     riderEmail,  // Now a string
	}

	if err := config.DB.Create(&booking).Error; err != nil {
		log.Println("Create booking error:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not create booking"})
		return
	}

	// Get driver's device token from new drivers table
	var driverToken string
	config.DB.Raw("SELECT device_token FROM drivers WHERE id = ?", req.DriverID).Scan(&driverToken)

	fareRange := fmt.Sprintf("GHS %.1f-%.1f", booking.FareLow, booking.FareHigh)
	
	// Now passing strings directly (no interface{})
	SendBookingNotification(
		driverToken, 
		booking.ID, 
		booking.SessionID, 
		booking.PickupAddress,
		booking.DropoffAddress, 
		fareRange, 
		booking.PickupLat, 
		booking.PickupLng,
		booking.DropoffLat, 
		booking.DropoffLng, 
		booking.DistanceKm, 
		booking.DurationText, 
		riderEmail, // Now a string
		riderID,    // Now a string
	)

	// Lock driver permanently
	config.DB.Model(&models.DriverAssignment{}).
		Where("session_id = ? AND driver_id = ?", req.SessionID, req.DriverID).
		Update("expires_at", time.Now().AddDate(1, 0, 0))

	// Get driver details from new drivers table
	var driver models.DriverResponse
	config.DB.Raw(`
		SELECT 
			d.id, 
			COALESCE(d.first_name, 'Driver') as username,
			d.phone,
			COALESCE(d.avatar_url, '') as avatar,
			COALESCE(dv.car_make, '') as car_make,
			COALESCE(dv.car_model, '') as car_model,
			COALESCE(dv.year, '') as year,
			COALESCE(dv.bay, '') as bay,
			COALESCE(dv.plate_number, '') as plate_number
		FROM drivers d
		LEFT JOIN driver_vehicles dv ON dv.driver_id = d.id AND dv.is_active = 1
		WHERE d.id = ?
	`, req.DriverID).Scan(&driver)

	baseURL := os.Getenv("APP_URL")
	if driver.Avatar != "" && !strings.HasPrefix(driver.Avatar, "http") {
		driver.Avatar = baseURL + "/images/avatar/" + driver.Avatar
	}

	// Send booking confirmation email
	if emailService != nil {
		// Convert booking.ID (uint) to string
		bookingIDStr := fmt.Sprintf("%d", booking.ID)
		
		var mapURL = buildMapURL(booking.PickupLat, booking.PickupLng, booking.DropoffLat, booking.DropoffLng)

		// Send the email
		err := emailService.SendBookingConfirmation(
			riderEmail, // Now a string
			bookingIDStr,
			booking.PickupAddress,
			booking.DropoffAddress,
			time.Now().Format("Jan 2, 2006 at 3:04 PM"),
			fmt.Sprintf("GHS %.2f - %.2f", booking.FareLow, booking.FareHigh),
			mapURL,
		)
		if err != nil {
			log.Printf("Failed to send booking confirmation email: %v", err)
			// Don't fail the booking if email fails
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "Booking confirmed",
		"booking_id": booking.ID,
		"driver":     driver,
		"booking": gin.H{
			"pickup":      booking.PickupAddress,
			"dropoff":     booking.DropoffAddress,
			"distance_km": booking.DistanceKm,
			"duration":    booking.DurationText,
			"fare_low":    booking.FareLow,
			"fare_high":   booking.FareHigh,
			"currency":    "GHS",
			"status":      booking.Status,
		},
	})
}

type CancelRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

func CancelBooking(c *gin.Context) {
	var req CancelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing required fields"})
		return
	}
	sessionID := req.SessionID

	var booking models.Booking
	if err := config.DB.Where("session_id = ? AND status IN ('pending','confirmed')",
		req.SessionID).First(&booking).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No active booking to cancel"})
		return
	}

	config.DB.Model(&booking).Update("status", "cancelled")
	config.DB.Where("driver_id = ? AND session_id = ?",
		booking.DriverID, sessionID).Delete(&models.DriverAssignment{})

	c.JSON(http.StatusOK, gin.H{"message": "Booking cancelled"})
}