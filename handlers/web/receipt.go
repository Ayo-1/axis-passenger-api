package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"goapi/config"
	"goapi/models"
)

const receiptGrace = 24 * time.Hour
const receiptTTL = 7 * 24 * time.Hour

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Shared: reference + contact (email or last-4-digits phone match), mirrors LookupBooking's rule.
// Shared: reference + contact (email or last-4-digits phone match), mirrors LookupBooking's rule.
func findBookingByContact(reference, contact string) (*models.BookingSchedule, error) {
	var booking models.BookingSchedule
	
	// Clean the reference (remove spaces, uppercase)
	reference = strings.TrimSpace(strings.ToUpper(reference))
	
	// Reject internal suffixes
	if strings.HasSuffix(reference, "-OUT") || strings.HasSuffix(reference, "-RTN") {
		return nil, fmt.Errorf("invalid booking reference")
	}
	
	// Try exact match first (single leg or rental)
	err := config.DB.Where(
		"session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
		reference, contact, contact,
	).First(&booking).Error
	
	if err == nil {
		return &booking, nil
	}
	
	// Try round trip outbound leg
	err = config.DB.Where(
		"session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
		reference+"-OUT", contact, contact,
	).First(&booking).Error
	
	if err != nil {
		return nil, err
	}
	
	return &booking, nil
}

func receiptPayload(b *models.BookingSchedule) gin.H {
	// For round trips, use the parent reference (strip -OUT/-RTN suffix)
	reference := b.SessionID
	if strings.Contains(reference, "-OUT") {
		reference = strings.TrimSuffix(reference, "-OUT")
	} else if strings.Contains(reference, "-RTN") {
		reference = strings.TrimSuffix(reference, "-RTN")
	}
	
	payload := gin.H{
		"reference":          reference,  // Use parent reference
		"serviceType":        b.ServiceType,
		"channel":            b.Channel,
		"status":             b.Status,
		"paymentStatus":      b.PaymentStatus,
		"paymentMethod":      b.PaymentMode,
		"tripType":           b.TripType,
		"airport":            b.Airport,
		"pickupLabel":        b.PickupAddress,
		"pickupLat":          b.PickupLat,
		"pickupLng":          b.PickupLng,
		"returnLabel":        b.ReturnPickupAddress,
		"returnLat":          b.ReturnPickupLat,
		"returnLng":          b.ReturnPickupLng,
		"flightNumber":       b.FlightNumber,
		"scheduledAt":        b.ScheduledAt,
		"returnFlightNumber": b.ReturnFlightNumber,
		"returnScheduledAt":  b.ReturnScheduledAt,
		"passengers":         b.Passengers,
		"luggage":            b.Luggage,
		"tierId":             b.TierID,
		"rentalDays":         b.RentalDays,
		"fareTotal":          b.FareTotal,
		"guestName":          b.GuestName,
		"guestPhone":         b.GuestPhone,
		"guestEmail":         b.GuestEmail,
		"issuedAt":           b.CreatedAt,
		"driverId":           b.DriverID,
		"driverStatus":       b.DriverStatus,
		"trackFlight":        b.TrackFlight,
		"notes":              b.Notes,
		"distanceKm":         b.DistanceKm,
		"durationText":       b.DurationText,
		"finalFare":          b.FinalFare,
		"createdAt":          b.CreatedAt,
		"updatedAt":          b.UpdatedAt,
		"processingFee": 	  b.ProcessingFee,
		"protocolFee":        b.ProtocolFee,
	}
	
	// Add driver details if driver is assigned
	if b.DriverID != "" {
		payload["driver"] = getDriverDetails(b.DriverID)
	}
	
	return payload
}

type ReceiptLookupRequest struct {
	Reference string `json:"reference" binding:"required"`
	Contact   string `json:"contact" binding:"required"`
}

// POST /app/bookings/receipt
func GetBookingReceipt(c *gin.Context) {
	var req ReceiptLookupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Clean the reference
	reference := strings.TrimSpace(strings.ToUpper(req.Reference))
	
	booking, err := findBookingByContact(reference, req.Contact)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "We couldn't find a booking with those details."})
		return
	}

	// Check if round trip
	if strings.Contains(booking.SessionID, "-OUT") {
		baseReference := strings.TrimSuffix(booking.SessionID, "-OUT")
		
		// Get return leg
		var returnBooking models.BookingSchedule
		config.DB.Where("session_id = ?", baseReference+"-RTN").First(&returnBooking)
		
		// Build combined payload
		combinedPayload := receiptPayload(booking)
		
		if returnBooking.ID != 0 {
			combinedPayload["returnFlightNumber"] = returnBooking.FlightNumber
			combinedPayload["returnScheduledAt"] = returnBooking.ScheduledAt
			combinedPayload["returnPickupAddress"] = returnBooking.PickupAddress
			combinedPayload["returnPickupLat"] = returnBooking.PickupLat
			combinedPayload["returnPickupLng"] = returnBooking.PickupLng
			combinedPayload["returnDropoffAddress"] = returnBooking.DropoffAddress
			combinedPayload["returnDropoffLat"] = returnBooking.DropoffLat
			combinedPayload["returnDropoffLng"] = returnBooking.DropoffLng
			combinedPayload["fareTotal"] = booking.FareTotal + returnBooking.FareTotal
			combinedPayload["tripType"] = "both"
		}
		
		c.JSON(http.StatusOK, gin.H{"status": "success", "data": combinedPayload})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": receiptPayload(booking)})
}

// POST /app/bookings/receipt/link — issues a one-time secure receipt link
// POST /app/bookings/receipt/link — issues a one-time secure receipt link
func CreateReceiptLink(c *gin.Context) {
	var req ReceiptLookupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Clean the reference
	reference := strings.TrimSpace(strings.ToUpper(req.Reference))
	
	booking, err := findBookingByContact(reference, req.Contact)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "We couldn't find a booking with those details."})
		return
	}

	token := randomToken()
	rt := models.ReceiptToken{
		ID:        uuid.New().String(),
		BookingID: booking.ID,
		Reference: reference,  // Store parent reference, not -OUT suffix
		TokenHash: hashToken(token),
		ExpiresAt: time.Now().Add(receiptTTL),
	}
	if err := config.DB.Create(&rt).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create receipt link"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"link":   "/receipt?token=" + token,
	})
}

type ReceiptTokenRequest struct {
	Token string `json:"token" binding:"required"`
}

// POST /app/bookings/receipt/token — consumes a one-time link
// POST /app/bookings/receipt/token — consumes a one-time link
func GetReceiptByToken(c *gin.Context) {
	var req ReceiptTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var rt models.ReceiptToken
	if err := config.DB.Where("token_hash = ?", hashToken(req.Token)).First(&rt).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "This receipt link is not valid."})
		return
	}

	if time.Now().After(rt.ExpiresAt) {
		c.JSON(http.StatusGone, gin.H{"error": "This receipt link has expired. Open your receipt from \"Manage booking\" instead."})
		return
	}

	if rt.UsedAt != nil && time.Since(*rt.UsedAt) > receiptGrace {
		c.JSON(http.StatusGone, gin.H{"error": "This receipt link has already been used. Open your receipt from \"Manage booking\" instead."})
		return
	}

	if rt.UsedAt == nil {
		now := time.Now()
		config.DB.Model(&rt).Update("used_at", now)
	}

	var booking models.BookingSchedule
	if err := config.DB.First(&booking, rt.BookingID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "This booking no longer exists."})
		return
	}

	// Check if round trip
	if strings.Contains(booking.SessionID, "-OUT") {
		baseReference := strings.TrimSuffix(booking.SessionID, "-OUT")
		
		var returnBooking models.BookingSchedule
		config.DB.Where("session_id = ?", baseReference+"-RTN").First(&returnBooking)
		
		combinedPayload := receiptPayload(&booking)
		
		if returnBooking.ID != 0 {
			combinedPayload["returnFlightNumber"] = returnBooking.FlightNumber
			combinedPayload["returnScheduledAt"] = returnBooking.ScheduledAt
			combinedPayload["returnPickupAddress"] = returnBooking.PickupAddress
			combinedPayload["returnPickupLat"] = returnBooking.PickupLat
			combinedPayload["returnPickupLng"] = returnBooking.PickupLng
			combinedPayload["returnDropoffAddress"] = returnBooking.DropoffAddress
			combinedPayload["returnDropoffLat"] = returnBooking.DropoffLat
			combinedPayload["returnDropoffLng"] = returnBooking.DropoffLng
			combinedPayload["fareTotal"] = booking.FareTotal + returnBooking.FareTotal
			combinedPayload["tripType"] = "both"
		}
		
		c.JSON(http.StatusOK, gin.H{"status": "success", "data": combinedPayload})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": receiptPayload(&booking)})
}