package web

import (
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"goapi/config"
	"goapi/models"
	"goapi/services"
)

var (
	PaystackService *services.PaystackService
	StripeService   *services.StripeService
)

// Optional helper – makes it nicer
func InitPaymentServices(paystack *services.PaystackService, stripe *services.StripeService) {
	PaystackService = paystack
	StripeService = stripe
}
type EstimateRequest struct {
	TripType          string  `json:"tripType" binding:"required,oneof=pickup dropoff both"`
	Airport           string  `json:"airport"`
	AirportID         string  `json:"airportId" binding:"required"`
	MainLocationLabel string  `json:"mainLocationLabel" binding:"required"`
	MainLocationLat   float64 `json:"mainLocationLat" binding:"required"`
	MainLocationLng   float64 `json:"mainLocationLng" binding:"required"`
	ReturnLocationLabel string `json:"returnLocationLabel"`
	ReturnLocationLat float64 `json:"returnLocationLat"`
	ReturnLocationLng float64 `json:"returnLocationLng"`
	Passengers        int     `json:"passengers" binding:"required,min=1,max=8"`
	Luggage           int     `json:"luggage" binding:"min=0,max=8"`
	TierID            string  `json:"tierId" binding:"required"`
	MeetGreet         bool    `json:"meetGreet"`
	ChildSeat         bool    `json:"childSeat"`
	ScheduledAt       string  `json:"scheduledAt" binding:"required"`
}

type DriverPreview struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	CarModel string `json:"carModel"`
	Plate    string `json:"plate"`
	TierCode string `json:"tierCode"`
}

type EstimateResponse struct {
	FareTotal        float64        `json:"fareTotal"`
	Currency         string         `json:"currency"`
	DistanceKm       float64        `json:"distanceKm"`
	ExtrasTotal      float64        `json:"extrasTotal"`
	Driver           *DriverPreview `json:"driver"`
	AvailableDrivers int            `json:"availableDrivers"`
}

func CreateEstimate(c *gin.Context) {
	var req EstimateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	scheduledTime, err := time.Parse(time.RFC3339, req.ScheduledAt)
	if err != nil || scheduledTime.Before(time.Now().Add(55*time.Minute)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scheduledAt must be at least 55 minutes in the future"})
		return
	}

	// Get tier
	var tier models.VehicleTier
	if err := config.DB.Raw(`
		SELECT id, name, code, passengers, luggage FROM vehicle_tiers 
		WHERE id = ? AND is_active = 1
	`, req.TierID).Scan(&tier).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tier"})
		return
	}

	// Validate tier was actually found
	if tier.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tier ID"})
		return
	}

	// Get fare config
	var fareConfig struct {
		BaseFare    float64 `gorm:"column:base_fare"`
		PricePerKm  float64 `gorm:"column:price_per_km"`
		MinimumFare float64 `gorm:"column:minimum_fare"`
	}
	config.DB.Raw(`
		SELECT 
			MAX(CASE WHEN config_key = 'base_fare' THEN CAST(config_value AS DECIMAL(10,2)) END) as base_fare,
			MAX(CASE WHEN config_key = 'price_per_km' THEN CAST(config_value AS DECIMAL(10,2)) END) as price_per_km,
			MAX(CASE WHEN config_key = 'minimum_fare' THEN CAST(config_value AS DECIMAL(10,2)) END) as minimum_fare
		FROM app_config
		WHERE config_key IN ('base_fare', 'price_per_km', 'minimum_fare')
	`).Scan(&fareConfig)

	// Get airport coordinates
	var airport struct {
		Lat float64
		Lng float64
	}
	if err := config.DB.Raw(`SELECT lat, lng FROM airports WHERE id = ? AND is_active = 1`, req.AirportID).Scan(&airport).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "airport not found"})
		return
	}

	// Validate airport was actually found
	if airport.Lat == 0 && airport.Lng == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid airport ID"})
		return
	}

	// Calculate distance based on trip type
	var distanceKm float64

	switch req.TripType {
	case "pickup":
		// Airport → destination
		distanceKm = haversine(airport.Lat, airport.Lng, req.MainLocationLat, req.MainLocationLng)

	case "dropoff":
		// Pickup point → Airport
		distanceKm = haversine(req.MainLocationLat, req.MainLocationLng, airport.Lat, airport.Lng)

	case "both":
		// Leg 1: Airport → destination
		leg1 := haversine(airport.Lat, airport.Lng, req.MainLocationLat, req.MainLocationLng)

		// Leg 2: Return point (or same destination) → Airport
		returnLat := req.MainLocationLat
		returnLng := req.MainLocationLng
		if req.ReturnLocationLat != 0 && req.ReturnLocationLng != 0 {
			returnLat = req.ReturnLocationLat
			returnLng = req.ReturnLocationLng
		}
		leg2 := haversine(returnLat, returnLng, airport.Lat, airport.Lng)

		distanceKm = leg1 + leg2
	}

	// Calculate extras
	extrasTotal := 0.0
	if req.MeetGreet {
		extrasTotal += 80
	}
	if req.ChildSeat {
		extrasTotal += 40
	}

	// Base fare
	baseFare := fareConfig.BaseFare + (distanceKm * fareConfig.PricePerKm)
	if baseFare < fareConfig.MinimumFare {
		baseFare = fareConfig.MinimumFare
	}

	fareTotal := baseFare + extrasTotal

	// Find available driver
	var driver DriverPreview
	err = config.DB.Raw(`
		SELECT d.id, CONCAT(COALESCE(d.first_name, ''), ' ', COALESCE(d.last_name, '')) as name, d.phone,
			COALESCE(dv.car_model, '') as carModel, COALESCE(dv.plate_number, '') as plate, 
			COALESCE(dv.tier_id, '') as tierCode
		FROM drivers d
		JOIN driver_vehicles dv ON dv.driver_id = d.id AND dv.is_active = 1
		WHERE d.is_active = 1 
		AND d.account_status = 'active'
		AND dv.tier_id = ?
		AND d.id NOT IN (
			SELECT driver_id FROM bookings 
			WHERE scheduled_at BETWEEN ? AND ?
			AND status IN ('confirmed', 'pending')
			AND driver_id IS NOT NULL
		)
		ORDER BY RAND()
		LIMIT 1
	`, req.TierID, scheduledTime.Add(-2*time.Hour), scheduledTime.Add(2*time.Hour)).Scan(&driver).Error

	var availableCount int
	config.DB.Raw(`
		SELECT COUNT(*) FROM drivers d
		JOIN driver_vehicles dv ON dv.driver_id = d.id AND dv.is_active = 1
		WHERE d.is_active = 1 
		AND d.account_status = 'active'
		AND dv.tier_id = ?
	`, req.TierID).Scan(&availableCount)

	if err != nil {
		c.JSON(http.StatusOK, EstimateResponse{
			FareTotal:        fareTotal,
			Currency:         "GHS",
			DistanceKm:       distanceKm,
			ExtrasTotal:      extrasTotal,
			Driver:           nil,
			AvailableDrivers: 0,
		})
		return
	}

	c.JSON(http.StatusOK, EstimateResponse{
		FareTotal:        fareTotal,
		Currency:         "GHS",
		DistanceKm:       distanceKm,
		ExtrasTotal:      extrasTotal,
		Driver:           &driver,
		AvailableDrivers: availableCount,
	})
}

func haversine(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}

// ── Change Driver ────────────────────────────────────────────

type ChangeDriverRequest struct {
	TierCode        string `json:"tierCode" binding:"required"`
	CurrentDriverID string `json:"currentDriverId" binding:"required"`
	ScheduledAt     string `json:"scheduledAt" binding:"required"`
}

func ChangeDriver(c *gin.Context) {
	var req ChangeDriverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	scheduledTime, err := time.Parse(time.RFC3339, req.ScheduledAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid scheduledAt"})
		return
	}

	var driver DriverPreview
	err = config.DB.Raw(`
		SELECT d.id, CONCAT(COALESCE(d.first_name, ''), ' ', COALESCE(d.last_name, '')) as name, d.phone,
		       COALESCE(dv.car_model, '') as carModel, COALESCE(dv.plate_number, '') as plate, 
		       COALESCE(dv.tier_id, '') as tierCode
		FROM drivers d
		JOIN driver_vehicles dv ON dv.driver_id = d.id AND dv.is_active = 1
		WHERE d.is_active = 1 
		AND d.account_status = 'active'
		AND dv.tier_id = ?
		AND d.id != ?
		AND d.id NOT IN (
			SELECT driver_id FROM bookings 
			WHERE scheduled_at BETWEEN ? AND ?
			AND status IN ('confirmed', 'pending')
			AND driver_id IS NOT NULL
		)
		ORDER BY RAND()
		LIMIT 1
	`, req.TierCode, req.CurrentDriverID, scheduledTime.Add(-2*time.Hour), scheduledTime.Add(2*time.Hour)).Scan(&driver).Error

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no alternative driver available"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"driver": driver})
}

// ── Create Booking ───────────────────────────────────────────

type CreateBookingRequest struct {
	ServiceType        string  `json:"serviceType" binding:"required,oneof=ride"`
	Channel            string  `json:"channel" binding:"required,oneof=direct partner"`
	GuestName          string  `json:"guestName" binding:"required,min=2"`
	GuestPhone         string  `json:"guestPhone" binding:"required,min=9"`
	GuestEmail         string  `json:"guestEmail" binding:"required,email"`
	TripType           string  `json:"tripType" binding:"required,oneof=pickup dropoff both"`
	AirportID          string  `json:"airportId" binding:"required"`
	Airport            string  `json:"airport" binding:"required"`
	MainLocationLabel  string  `json:"mainLocationLabel" binding:"required"`
	MainLocationLat    float64 `json:"mainLocationLat" binding:"required"`
	MainLocationLng    float64 `json:"mainLocationLng" binding:"required"`
	ReturnLocationLabel string `json:"returnLocationLabel"`
	ReturnLocationLat  float64 `json:"returnLocationLat"`
	ReturnLocationLng  float64 `json:"returnLocationLng"`
	FlightNumber       string  `json:"flightNumber"`
	ScheduledAt        string  `json:"scheduledAt" binding:"required"`
	ReturnFlightNumber string  `json:"returnFlightNumber"`
	ReturnScheduledAt  string  `json:"returnScheduledAt"`
	Passengers         int     `json:"passengers" binding:"required,min=1,max=8"`
	Luggage            int     `json:"luggage" binding:"min=0,max=8"`
	TierID             string  `json:"tierId" binding:"required"`
	RentalDays         int     `json:"rentalDays"`
	Extras             []Extra `json:"extras"`
	PaymentMethod      string  `json:"paymentMethod" binding:"required,oneof=paystack stripe"`
	DriverID           string  `json:"driverId" binding:"required"`
	FareTotal          float64 `json:"fareTotal" binding:"required"`
	Notes              string  `json:"notes"`
}

type Extra struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
}

func CreateBooking(c *gin.Context) {
	var req CreateBookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate airport exists + get coords
	var airportCoords struct {
		Lat float64
		Lng float64
	}
	if err := config.DB.Raw(`SELECT lat, lng FROM airports WHERE id = ? AND is_active = 1`, req.AirportID).Scan(&airportCoords).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid airport ID"})
		return
	}
	if airportCoords.Lat == 0 && airportCoords.Lng == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid airport ID"})
		return
	}

	// Validate tier exists
	var tierExists int
	config.DB.Raw(`SELECT COUNT(*) FROM vehicle_tiers WHERE id = ? AND is_active = 1`, req.TierID).Scan(&tierExists)
	if tierExists == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tier ID"})
		return
	}

	// Validate driver exists, active, correct tier, and available at this time
	var driverExists int
	config.DB.Raw(`
		SELECT COUNT(*) FROM drivers d
		JOIN driver_vehicles dv ON dv.driver_id = d.id AND dv.is_active = 1
		WHERE d.id = ? AND d.is_active = 1 AND d.account_status = 'active'
		AND dv.tier_id = ?
	`, req.DriverID, req.TierID).Scan(&driverExists)
	if driverExists == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid driver or driver not available for this tier"})
		return
	}

	scheduledTime, err := time.Parse(time.RFC3339, req.ScheduledAt)
	if err != nil || scheduledTime.Before(time.Now().Add(55*time.Minute)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scheduledAt must be at least 55 minutes in the future"})
		return
	}

	var returnTime *time.Time
	if req.TripType == "both" {
		parsed, err := time.Parse(time.RFC3339, req.ReturnScheduledAt)
		if err != nil || parsed.Before(scheduledTime.Add(3*time.Hour)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "returnScheduledAt must be at least 3 hours after scheduledAt"})
			return
		}
		returnTime = &parsed
	}

	// ── Map pickup/dropoff correctly ──
	var pickupLat, pickupLng, dropoffLat, dropoffLng float64
	var pickupLabel, dropoffLabel string

	switch req.TripType {
	case "pickup":
		// Airport → mainLocation
		pickupLat = airportCoords.Lat
		pickupLng = airportCoords.Lng
		pickupLabel = req.Airport
		dropoffLat = req.MainLocationLat
		dropoffLng = req.MainLocationLng
		dropoffLabel = req.MainLocationLabel

	case "dropoff":
		// mainLocation → Airport
		pickupLat = req.MainLocationLat
		pickupLng = req.MainLocationLng
		pickupLabel = req.MainLocationLabel
		dropoffLat = airportCoords.Lat
		dropoffLng = airportCoords.Lng
		dropoffLabel = req.Airport

	case "both":
		// Leg 1: Airport → mainLocation
		pickupLat = airportCoords.Lat
		pickupLng = airportCoords.Lng
		pickupLabel = req.Airport
		dropoffLat = req.MainLocationLat
		dropoffLng = req.MainLocationLng
		dropoffLabel = req.MainLocationLabel
	}

	// ── Map return leg for round trip ──
	var returnPickupLat, returnPickupLng, returnDropoffLat, returnDropoffLng float64
	var returnPickupAddress, returnDropoffAddress string

	if req.TripType == "both" {
		// Return leg: returnLocation (or mainLocation) → Airport
		returnPickupLat = req.ReturnLocationLat
		returnPickupLng = req.ReturnLocationLng
		returnPickupAddress = req.ReturnLocationLabel

		if returnPickupLat == 0 && returnPickupLng == 0 {
			// Same as main location
			returnPickupLat = req.MainLocationLat
			returnPickupLng = req.MainLocationLng
			returnPickupAddress = req.MainLocationLabel
		}

		returnDropoffLat = airportCoords.Lat
		returnDropoffLng = airportCoords.Lng
		returnDropoffAddress = req.Airport
	}

	reference := generateBookingReference()

	booking := models.BookingSchedule{
		SessionID:           reference,
		DriverID:            req.DriverID,
		ServiceType:         req.ServiceType,
		Channel:             req.Channel,
		GuestName:           req.GuestName,
		GuestPhone:          req.GuestPhone,
		GuestEmail:          req.GuestEmail,
		TripType:            req.TripType,
		Airport:             req.Airport,
		PickupAddress:       pickupLabel,
		PickupLat:           pickupLat,
		PickupLng:           pickupLng,
		DropoffAddress:      dropoffLabel,
		DropoffLat:          dropoffLat,
		DropoffLng:          dropoffLng,
		FlightNumber:        req.FlightNumber,
		ScheduledAt:         &scheduledTime,
		ReturnFlightNumber:  req.ReturnFlightNumber,
		ReturnScheduledAt:   returnTime,
		ReturnPickupAddress: returnPickupAddress,
		ReturnPickupLat:     returnPickupLat,
		ReturnPickupLng:     returnPickupLng,
		ReturnDropoffAddress: returnDropoffAddress,
		ReturnDropoffLat:    returnDropoffLat,
		ReturnDropoffLng:    returnDropoffLng,
		Passengers:          req.Passengers,
		Luggage:             req.Luggage,
		TierID:              req.TierID,
		FareTotal:           req.FareTotal,
		PaymentMode:         req.PaymentMethod,
		PaymentStatus:       "pending",
		Status:              "pending",
	}

	if err := config.DB.Create(&booking).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create booking"})
		return
	}

	// ── Generate payment link ──
	var paymentURL string
	var stripeSessionID string

	if req.PaymentMethod == "paystack" {
		resp, err := PaystackService.GeneratePaymentLink(services.PaymentLinkRequest{
			Amount:      req.FareTotal,
			Email:       req.GuestEmail,
			Description: "Axis Booking - " + reference,
			Reference:   reference,
			Metadata: map[string]interface{}{
				"type":       "booking_payment",
				"booking_id": booking.ID,
				"reference":  reference,
			},
			CallbackURL: "https://your-api.com/v1/bookings/payment/callback",
		})
		if err == nil {
			paymentURL = resp.PaymentURL
		} else {
			config.DB.Delete(&booking)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate payment link"})
			return
		}
	} else {
		resp, err := StripeService.GeneratePaymentLink(
			req.FareTotal,
			"USD",
			req.GuestEmail,
			"Axis Booking - "+reference,
			reference,
			map[string]string{
				"booking_id": fmt.Sprintf("%d", booking.ID),
				"reference":  reference,
			},
		)
		if err == nil {
			paymentURL = resp.PaymentURL
			stripeSessionID = resp.SessionID
		} else {
			config.DB.Delete(&booking)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate payment link"})
			return
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"status": "success",
		"data": gin.H{
			"id":                booking.ID,
			"reference":         reference,
			"paymentUrl":        paymentURL,
			"provider":          req.PaymentMethod,
			"stripe_session_id": stripeSessionID,
			"status":            "pending",
			"paymentStatus":     "pending",
			"fareTotal":         req.FareTotal,
			"currency":          "GHS",
			"manageUrl":         "/manage?ref=" + reference,
			"createdAt":         time.Now(),
		},
	})
}

// ── Lookup/Update/Cancel — same as before, unchanged ─────────

type LookupBookingRequest struct {
	Reference string `json:"reference" binding:"required"`
	Contact   string `json:"contact" binding:"required"`
}

func LookupBooking(c *gin.Context) {
	var req LookupBookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var booking models.BookingSchedule
	err := config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
		req.Reference, req.Contact, req.Contact).First(&booking).Error

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": booking})
}

type UpdateBookingRequest struct {
	Reference          string `json:"reference" binding:"required"`
	Contact            string `json:"contact" binding:"required"`
	FlightNumber       string `json:"flightNumber"`
	ScheduledAt        string `json:"scheduledAt"`
	ReturnFlightNumber string `json:"returnFlightNumber"`
	ReturnScheduledAt  string `json:"returnScheduledAt"`
}

func UpdateBooking(c *gin.Context) {
	var req UpdateBookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var booking models.BookingSchedule
	err := config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
		req.Reference, req.Contact, req.Contact).First(&booking).Error

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}

	updates := make(map[string]interface{})

	if req.FlightNumber != "" {
		updates["flight_number"] = req.FlightNumber
	}
	if req.ScheduledAt != "" {
		scheduledTime, err := time.Parse(time.RFC3339, req.ScheduledAt)
		if err != nil || scheduledTime.Before(time.Now().Add(55*time.Minute)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scheduledAt must be at least 55 minutes in the future"})
			return
		}
		updates["scheduled_at"] = scheduledTime
	}
	if req.ReturnFlightNumber != "" {
		updates["return_flight_number"] = req.ReturnFlightNumber
	}
	if req.ReturnScheduledAt != "" {
		returnTime, err := time.Parse(time.RFC3339, req.ReturnScheduledAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid returnScheduledAt"})
			return
		}
		updates["return_scheduled_at"] = returnTime
	}

	if len(updates) > 0 {
		config.DB.Model(&booking).Updates(updates)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "booking updated"})
}

type CancelBookingRequest struct {
	Reference string `json:"reference" binding:"required"`
	Contact   string `json:"contact" binding:"required"`
}

func CancelBooking(c *gin.Context) {
	var req CancelBookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var booking models.Booking
	err := config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
		req.Reference, req.Contact, req.Contact).First(&booking).Error

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}

	if booking.Status == "completed" || booking.Status == "cancelled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "booking already " + booking.Status})
		return
	}

	config.DB.Model(&booking).Update("status", "cancelled")

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "booking cancelled"})
}

// POST /v1/bookings/verify-payment
func VerifyBookingPayment(c *gin.Context) {
	var req struct {
		Reference string `json:"reference" binding:"required"`
		Provider  string `json:"provider" binding:"required,oneof=paystack stripe"`
		SessionID string `json:"sessionId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check current booking status first
	var booking models.BookingSchedule
	if err := config.DB.Where("session_id = ?", req.Reference).First(&booking).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}

	// Already paid?
	if booking.PaymentStatus == "paid" {
		c.JSON(http.StatusOK, gin.H{
			"status":        "paid",
			"paymentStatus": booking.PaymentStatus,
			"bookingStatus": booking.Status,
		})
		return
	}

	// Verify with provider
	var success bool
	var err error

	switch req.Provider {
	case "paystack":
		success, _, _, err = PaystackService.VerifyTransaction(req.Reference)
	case "stripe":
		if req.SessionID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId required for stripe"})
			return
		}
		success, _, err = StripeService.VerifySession(req.SessionID)
	}

	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "verification failed"})
		return
	}

	if !success {
		c.JSON(http.StatusOK, gin.H{
			"status":        "pending",
			"paymentStatus": "pending",
			"bookingStatus": booking.Status,
		})
		return
	}

	// Payment confirmed — update booking
	config.DB.Model(&booking).Updates(map[string]interface{}{
		"payment_status": "paid",
		"status":         "confirmed",
		"driver_status":  "assigned",
	})
	

	// Notify driver
	// go notifyDriver(booking)

	c.JSON(http.StatusOK, gin.H{
		"status":        "paid",
		"paymentStatus": "paid",
		"bookingStatus": "confirmed",
	})
}


type CreateRentalRequest struct {
	CarID          string  `json:"carId" binding:"required"`
	GuestName      string  `json:"guestName" binding:"required,min=2"`
	GuestPhone     string  `json:"guestPhone" binding:"required,min=9"`
	GuestEmail     string  `json:"guestEmail" binding:"required,email"`
	PickupDate     string  `json:"pickupDate" binding:"required"`     // ISO-8601
	ReturnDate     string  `json:"returnDate" binding:"required"`     // ISO-8601
	CollectionMethod string `json:"collectionMethod" binding:"required,oneof=hub_pickup delivery"`
	DeliveryAddress string `json:"deliveryAddress"`                    // Required if delivery
	PaymentMethod  string  `json:"paymentMethod" binding:"required"`  // paystack, stripe
	FareTotal      float64 `json:"fareTotal" binding:"required"`      // From frontend
}

func CreateRental(c *gin.Context) {
	var req CreateRentalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate car exists
	var car models.RentalCar
	if err := config.DB.Where("id = ? AND is_active = 1", req.CarID).First(&car).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid car"})
		return
	}

	// Validate dates
	pickupTime, err := time.Parse(time.RFC3339, req.PickupDate)
	if err != nil || pickupTime.Before(time.Now().Add(1*time.Hour)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pickupDate must be at least 1 hour in the future"})
		return
	}

	returnTime, err := time.Parse(time.RFC3339, req.ReturnDate)
	if err != nil || returnTime.Before(pickupTime.Add(24*time.Hour)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "returnDate must be at least 24 hours after pickupDate"})
		return
	}

	// Calculate rental days
	rentalDays := int(math.Ceil(returnTime.Sub(pickupTime).Hours() / 24))

	// Get delivery fee from config
	var deliveryFee float64
	config.DB.Raw(`SELECT CAST(config_value AS DECIMAL(10,2)) FROM app_config WHERE config_key = 'delivery_fee'`).Scan(&deliveryFee)

	// Calculate fare
	fareTotal := car.RentPerDay * float64(rentalDays)

	// Add delivery fee if applicable
	if req.CollectionMethod == "delivery" {
		if req.DeliveryAddress == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "deliveryAddress required for delivery"})
			return
		}
		fareTotal += deliveryFee
	}

	// Create booking with service_type = "rental"
	reference := generateBookingReference()

	pickupAddress := "Axis Hub"
	if req.CollectionMethod == "delivery" {
		pickupAddress = req.DeliveryAddress
	}

	booking := models.BookingSchedule{
		SessionID:      reference,
		ServiceType:    "rental",
		Channel:        "direct",
		GuestName:      req.GuestName,
		GuestPhone:     req.GuestPhone,
		GuestEmail:     req.GuestEmail,
		TripType:       "rental",
		TierID:         car.ID,          // Store rental car ID here
		RentalDays:     rentalDays,
		DeliveryOption: req.CollectionMethod,
		FareTotal:      fareTotal,
		PaymentMode:    req.PaymentMethod,
		PaymentStatus:  "pending",
		Status:         "pending",
		ScheduledAt:    &pickupTime,
		ReturnScheduledAt: &returnTime,
		PickupAddress:  pickupAddress,
		DropoffAddress: "Axis Hub", // return to hub
	}

	if err := config.DB.Create(&booking).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create rental booking"})
		return
	}

	// Generate payment link (same as ride booking)
	var paymentURL string
	var stripeSessionID string

	if req.PaymentMethod == "paystack" {
		resp, err := PaystackService.GeneratePaymentLink(services.PaymentLinkRequest{
			Amount:      fareTotal,
			Email:       req.GuestEmail,
			Description: "Axis Rental - " + reference,
			Reference:   reference,
			Metadata: map[string]interface{}{
				"type":       "rental_payment",
				"booking_id": booking.ID,
				"reference":  reference,
			},
		})
		if err == nil {
			paymentURL = resp.PaymentURL
		}
	} else {
		resp, err := StripeService.GeneratePaymentLink(
			fareTotal,
			"USD",
			req.GuestEmail,
			"Axis Rental - "+reference,
			reference,
			map[string]string{
				"booking_id": fmt.Sprintf("%d", booking.ID),
				"reference":  reference,
			},
		)
		if err == nil {
			paymentURL = resp.PaymentURL
			stripeSessionID = resp.SessionID
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"status": "success",
		"data": gin.H{
			"id":                booking.ID,
			"reference":         reference,
			"paymentUrl":        paymentURL,
			"provider":          req.PaymentMethod,
			"stripe_session_id": stripeSessionID,
			"status":            "pending",
			"paymentStatus":     "pending",
			"fareTotal":         fareTotal,
			"rentalDays":        rentalDays,
			"currency":          "GHS",
			"manageUrl":         "/manage?ref=" + reference,
			"createdAt":         time.Now(),
		},
	})
}

func generateBookingReference() string {
	for i := 0; i < 10; i++ {
		ref := fmt.Sprintf("AX-%06d", rand.Intn(900000000)+100000000)
		var count int64
		config.DB.Model(&models.BookingSchedule{}).Where("session_id = ?", ref).Count(&count)
		if count == 0 {
			return ref
		}
	}
	return fmt.Sprintf("AX-%d", time.Now().Unix()%1000000)
}