package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goapi/config"
	"goapi/handlers"
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
	TripType            string  `json:"tripType" binding:"required,oneof=pickup dropoff both"`
	Airport             string  `json:"airport"`
	AirportID           string  `json:"airportId" binding:"required"`
	MainLocationLabel   string  `json:"mainLocationLabel" binding:"required"`
	MainLocationLat     float64 `json:"mainLocationLat" binding:"required"`
	MainLocationLng     float64 `json:"mainLocationLng" binding:"required"`
	ReturnLocationLabel string  `json:"returnLocationLabel"`
	ReturnLocationLat   float64 `json:"returnLocationLat"`
	ReturnLocationLng   float64 `json:"returnLocationLng"`
	Passengers          int     `json:"passengers" binding:"required,min=1,max=8"`
	Luggage             int     `json:"luggage" binding:"min=0,max=8"`
	TierID              string  `json:"tierId" binding:"required"`
	MeetGreet           bool    `json:"meetGreet"`
	ChildSeat           bool    `json:"childSeat"`
	ScheduledAt         string  `json:"scheduledAt" binding:"required"`
	Protocol            bool    `json:"protocol"`
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
	ProcessingFee    float64        `json:"processingFee"`
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
	// Round distance to 1 decimal place
	distanceKm = math.Round(distanceKm*10) / 10

	// Calculate extras
	extrasTotal := 0.0

	// Platform fee (mandatory)
	const platformFee = 28.0
	extrasTotal += platformFee

	// Protocol fee: GHS 500 per person if selected
	if req.Protocol {
		extrasTotal += float64(req.Passengers) * 500
	}

	// Base fare
	baseFare := fareConfig.BaseFare + (distanceKm * fareConfig.PricePerKm)
	if baseFare < fareConfig.MinimumFare {
		baseFare = fareConfig.MinimumFare
	}

	// Round base fare to 2 decimal places
	baseFare = math.Round(baseFare*100) / 100

	fareTotal := baseFare + extrasTotal

	// Round final fare to 2 decimal places
	// fareTotal = math.Round(fareTotal*100) / 100

	// Round to whole number
	fareTotal = math.Round(fareTotal)

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
		AND d.phone != '550880119'
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
			ProcessingFee:    platformFee,
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
		ProcessingFee:    platformFee,
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
		AND d.phone != '550880119'
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
	ServiceType         string  `json:"serviceType" binding:"required,oneof=ride"`
	Channel             string  `json:"channel" binding:"required,oneof=direct partner"`
	GuestName           string  `json:"guestName" binding:"required,min=2"`
	GuestPhone          string  `json:"guestPhone" binding:"required,min=9"`
	GuestEmail          string  `json:"guestEmail" binding:"required,email"`
	TripType            string  `json:"tripType" binding:"required,oneof=pickup dropoff both"`
	AirportID           string  `json:"airportId" binding:"required"`
	Airport             string  `json:"airport" binding:"required"`
	MainLocationLabel   string  `json:"mainLocationLabel" binding:"required"`
	MainLocationLat     float64 `json:"mainLocationLat" binding:"required"`
	MainLocationLng     float64 `json:"mainLocationLng" binding:"required"`
	ReturnLocationLabel string  `json:"returnLocationLabel"`
	ReturnLocationLat   float64 `json:"returnLocationLat"`
	ReturnLocationLng   float64 `json:"returnLocationLng"`
	TrackFlight         bool    `json:"track_flight"`
	FlightNumber        string  `json:"flightNumber"`
	ScheduledAt         string  `json:"scheduledAt" binding:"required"`
	ReturnFlightNumber  string  `json:"returnFlightNumber"`
	ReturnScheduledAt   string  `json:"returnScheduledAt"`
	Passengers          int     `json:"passengers" binding:"required,min=1,max=8"`
	Luggage             int     `json:"luggage" binding:"min=0,max=8"`
	TierID              string  `json:"tierId" binding:"required"`
	RentalDays          int     `json:"rentalDays"`
	Extras              []Extra `json:"extras"`
	PaymentMethod       string  `json:"paymentMethod" binding:"required,oneof=paystack stripe"`
	DriverID            string  `json:"driverId" binding:"required"`
	FareTotal           float64 `json:"fareTotal" binding:"required"`
	Notes               string  `json:"notes"`
	HotelID             string  `json:"hotelId"`
	PaymentMode         string  `json:"paymentMode"`
	CollectionMethod    string  `json:"collectionMethod"`
	DeliveryAddress     string  `json:"deliveryAddress"`
	Protocol            bool    `json:"protocol"`
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

	req.Channel = "direct"
	req.HotelID = ""

	// Cap pending bookings per email to limit abuse
	var recentPending int64
	config.DB.Model(&models.BookingSchedule{}).
		Where("LOWER(guest_email) = LOWER(?) AND status = 'pending' AND created_at > ?",
			req.GuestEmail, time.Now().Add(-1*time.Hour)).
		Count(&recentPending)
	if recentPending >= 5 {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": "too many pending bookings — complete or cancel an existing one first",
		})
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
	var tier models.VehicleTier
	if err := config.DB.Raw(`
		SELECT id, passengers, luggage FROM vehicle_tiers WHERE id = ? AND is_active = 1
	`, req.TierID).Scan(&tier).Error; err != nil || tier.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tier ID"})
		return
	}
	if req.Passengers > tier.Passengers {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many passengers for this vehicle"})
		return
	}
	if req.Luggage > tier.Luggage {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too much luggage for this vehicle"})
		return
	}

	// Validate driver exists, active, correct tier
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

	// Reject if driver already has a booking in the ±2h window
	var driverConflicts int64
	config.DB.Model(&models.BookingSchedule{}).
		Where("driver_id = ? AND status IN ('confirmed','pending') AND scheduled_at BETWEEN ? AND ?",
			req.DriverID, scheduledTime.Add(-2*time.Hour), scheduledTime.Add(2*time.Hour)).
		Count(&driverConflicts)
	if driverConflicts > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "driver no longer available for this time"})
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

	if req.TripType == "both" && returnTime != nil {
		var returnConflicts int64
		config.DB.Model(&models.BookingSchedule{}).
			Where("driver_id = ? AND status IN ('confirmed','pending') AND scheduled_at BETWEEN ? AND ?",
				req.DriverID, returnTime.Add(-2*time.Hour), returnTime.Add(2*time.Hour)).
			Count(&returnConflicts)
		if returnConflicts > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "driver no longer available for the return leg"})
			return
		}
	}

	fare, err := computeFare(fareInput{
		AirportID:  req.AirportID,
		TripType:   req.TripType,
		MainLat:    req.MainLocationLat,
		MainLng:    req.MainLocationLng,
		ReturnLat:  req.ReturnLocationLat,
		ReturnLng:  req.ReturnLocationLng,
		Passengers: req.Passengers,
		Protocol:   req.Protocol,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unable to price this trip"})
		return
	}

	// Overwrite whatever the client sent. Never trust it.
	req.FareTotal = fare.Total

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
	// After calculating protocolFee:
	platformFee := fare.PlatformFee
	protocolFee := fare.ProtocolFee

	// ── Map return leg for round trip ──
	var returnPickupLat, returnPickupLng, returnDropoffLat, returnDropoffLng float64
	var returnPickupAddress, returnDropoffAddress string

	if req.TripType == "both" {
		returnPickupLat = req.ReturnLocationLat
		returnPickupLng = req.ReturnLocationLng
		returnPickupAddress = req.ReturnLocationLabel

		if returnPickupLat == 0 && returnPickupLng == 0 {
			returnPickupLat = req.MainLocationLat
			returnPickupLng = req.MainLocationLng
			returnPickupAddress = req.MainLocationLabel
		}

		returnDropoffLat = airportCoords.Lat
		returnDropoffLng = airportCoords.Lng
		returnDropoffAddress = req.Airport
	}

	reference := generateBookingReference()

	// Split fare for round trips (60% outbound, 40% return)
	outboundFare := req.FareTotal
	returnFare := 0.0
	if req.TripType == "both" {
		outboundFare = req.FareTotal * 0.6
		returnFare = req.FareTotal * 0.4
	}

	if req.TripType == "both" {
		// ── Create OUTBOUND booking ──
		outboundBooking := models.BookingSchedule{
			SessionID:      reference + "-OUT",
			DriverID:       req.DriverID,
			ServiceType:    req.ServiceType,
			Channel:        req.Channel,
			GuestName:      req.GuestName,
			GuestPhone:     req.GuestPhone,
			GuestEmail:     req.GuestEmail,
			TripType:       "pickup", // Outbound is always pickup from airport
			Airport:        req.Airport,
			PickupAddress:  pickupLabel,
			PickupLat:      pickupLat,
			PickupLng:      pickupLng,
			DropoffAddress: dropoffLabel,
			DropoffLat:     dropoffLat,
			DropoffLng:     dropoffLng,
			TrackFlight:    req.TrackFlight,
			FlightNumber:   req.FlightNumber,
			ScheduledAt:    &scheduledTime,
			Passengers:     req.Passengers,
			Luggage:        req.Luggage,
			TierID:         req.TierID,
			ProcessingFee:  platformFee / 2,
			ProtocolFee:    protocolFee,
			FareTotal:      outboundFare,
			PaymentMode:    req.PaymentMethod,
			PaymentStatus:  "pending",
			Status:         "pending",
			Notes:          req.Notes,
			DistanceKm:     fare.DistanceKm,
		}

		if err := config.DB.Create(&outboundBooking).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create outbound booking"})
			return
		}

		// ── Create RETURN booking ──
		returnBooking := models.BookingSchedule{
			SessionID:      reference + "-RTN",
			DriverID:       req.DriverID,
			ServiceType:    req.ServiceType,
			Channel:        req.Channel,
			GuestName:      req.GuestName,
			GuestPhone:     req.GuestPhone,
			GuestEmail:     req.GuestEmail,
			TripType:       "dropoff", // Return is always dropoff to airport
			Airport:        req.Airport,
			PickupAddress:  returnPickupAddress,
			PickupLat:      returnPickupLat,
			PickupLng:      returnPickupLng,
			DropoffAddress: returnDropoffAddress,
			DropoffLat:     returnDropoffLat,
			DropoffLng:     returnDropoffLng,
			TrackFlight:    req.TrackFlight,
			FlightNumber:   req.ReturnFlightNumber,
			ScheduledAt:    returnTime,
			Passengers:     req.Passengers,
			Luggage:        req.Luggage,
			TierID:         req.TierID,
			ProcessingFee:  platformFee / 2,
			ProtocolFee:    protocolFee,
			FareTotal:      returnFare,
			PaymentMode:    req.PaymentMethod,
			PaymentStatus:  "pending",
			Status:         "pending",
			Notes:          req.Notes,
			DistanceKm:     fare.DistanceKm,
		}

		if err := config.DB.Create(&returnBooking).Error; err != nil {
			// Rollback outbound if return fails
			config.DB.Delete(&outboundBooking)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create return booking"})
			return
		}

		res, err := createPaymentIntent(
			reference,
			req.PaymentMethod,
			req.GuestEmail,
			"Axis Booking - "+reference,
			req.FareTotal,
			os.Getenv("APP_URL_MAIN")+"/book?ref="+reference+"&provider="+req.PaymentMethod,
			os.Getenv("APP_URL_MAIN")+"/book?cancelled=1",
			map[string]interface{}{
				"type":              "booking_payment",
				"booking_id":        outboundBooking.ID,
				"return_booking_id": returnBooking.ID,
				"trip_type":         "both",
			},
		)
		if err != nil {
			config.DB.Delete(&outboundBooking)
			config.DB.Delete(&returnBooking)
			slog.Error("payment intent failed", "ref", reference, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate payment link"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"status": "success",
			"data": gin.H{
				"id":                outboundBooking.ID,
				"reference":         reference,
				"outboundReference": outboundBooking.SessionID,
				"returnReference":   returnBooking.SessionID,
				"paymentUrl":        res.PaymentURL,
				"provider":          req.PaymentMethod,
				"stripe_session_id": res.StripeSessionID,
				"status":            "pending",
				"paymentStatus":     "pending",
				"fareTotal":         req.FareTotal,
				"currency":          "GHS",
				"manageUrl":         "/manage?ref=" + reference,
				"createdAt":         time.Now(),
			},
		})
	} else {
		// ── Single leg booking (existing code) ──
		booking := models.BookingSchedule{
			SessionID:      reference,
			DriverID:       req.DriverID,
			ServiceType:    req.ServiceType,
			Channel:        req.Channel,
			GuestName:      req.GuestName,
			GuestPhone:     req.GuestPhone,
			GuestEmail:     req.GuestEmail,
			TripType:       req.TripType,
			Airport:        req.Airport,
			PickupAddress:  pickupLabel,
			PickupLat:      pickupLat,
			PickupLng:      pickupLng,
			DropoffAddress: dropoffLabel,
			DropoffLat:     dropoffLat,
			DropoffLng:     dropoffLng,
			TrackFlight:    req.TrackFlight,
			FlightNumber:   req.FlightNumber,
			ScheduledAt:    &scheduledTime,
			Passengers:     req.Passengers,
			Luggage:        req.Luggage,
			TierID:         req.TierID,
			ProcessingFee:  platformFee,
			ProtocolFee:    protocolFee,
			FareTotal:      req.FareTotal,
			PaymentMode:    req.PaymentMethod,
			PaymentStatus:  "pending",
			Status:         "pending",
			Notes:          req.Notes,
			DistanceKm:     fare.DistanceKm,
		}

		if err := config.DB.Create(&booking).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create booking"})
			return
		}

		res, err := createPaymentIntent(
			reference,
			req.PaymentMethod,
			req.GuestEmail,
			"Axis Booking - "+reference,
			req.FareTotal,
			os.Getenv("APP_URL_MAIN")+"/book?ref="+reference+"&provider="+req.PaymentMethod,
			os.Getenv("APP_URL_MAIN")+"/book?cancelled=1",
			map[string]interface{}{
				"type":       "booking_payment",
				"booking_id": booking.ID,
				"trip_type":  req.TripType,
			},
		)
		if err != nil {
			config.DB.Delete(&booking)
			slog.Error("payment intent failed", "ref", reference, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate payment link"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"status": "success",
			"data": gin.H{
				"id":                booking.ID,
				"reference":         reference,
				"paymentUrl":        res.PaymentURL,
				"provider":          req.PaymentMethod,
				"stripe_session_id": res.StripeSessionID,
				"status":            "pending",
				"paymentStatus":     "pending",
				"fareTotal":         req.FareTotal,
				"currency":          "GHS",
				"manageUrl":         "/manage?ref=" + reference,
				"createdAt":         time.Now(),
			},
		})
	}
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

	// Clean up the reference - remove any trailing spaces and convert to uppercase
	reference := strings.TrimSpace(strings.ToUpper(req.Reference))

	// Reject lookups with internal suffixes
	if strings.HasSuffix(reference, "-OUT") || strings.HasSuffix(reference, "-RTN") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid booking reference. Please use the reference from your confirmation email.",
		})
		return
	}

	// Try exact match first (single leg or rental)
	var booking models.BookingSchedule
	err := config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
		reference, req.Contact, req.Contact).First(&booking).Error

	if err != nil {
		// Try round trip outbound leg
		err = config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
			reference+"-OUT", req.Contact, req.Contact).First(&booking).Error

		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
	}

	// Check if round trip
	if strings.Contains(booking.SessionID, "-OUT") {
		baseReference := strings.TrimSuffix(booking.SessionID, "-OUT")

		// Get return leg
		var returnBooking models.BookingSchedule
		config.DB.Where("session_id = ?", baseReference+"-RTN").First(&returnBooking)

		// Build combined response with parent reference
		combinedPayload := receiptPayload(&booking)

		// Add return leg info to the SAME payload
		if returnBooking.ID != 0 {
			combinedPayload["returnFlightNumber"] = returnBooking.FlightNumber
			combinedPayload["returnScheduledAt"] = returnBooking.ScheduledAt
			combinedPayload["returnPickupAddress"] = returnBooking.PickupAddress
			combinedPayload["returnPickupLat"] = returnBooking.PickupLat
			combinedPayload["returnPickupLng"] = returnBooking.PickupLng
			combinedPayload["returnDropoffAddress"] = returnBooking.DropoffAddress
			combinedPayload["returnDropoffLat"] = returnBooking.DropoffLat
			combinedPayload["returnDropoffLng"] = returnBooking.DropoffLng

			// Combined fare
			combinedPayload["fareTotal"] = booking.FareTotal + returnBooking.FareTotal
			combinedPayload["tripType"] = "both"
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "success",
			"data":   combinedPayload,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "data": receiptPayload(&booking)})
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

	// First try to find the booking
	var booking models.BookingSchedule
	err := config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
		req.Reference, req.Contact, req.Contact).First(&booking).Error

	if err != nil {
		// Check if it's a round trip (try -OUT suffix)
		err = config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
			req.Reference+"-OUT", req.Contact, req.Contact).First(&booking).Error

		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
	}

	// Check if this is a round trip
	isRoundTrip := strings.Contains(booking.SessionID, "-OUT") || strings.Contains(booking.SessionID, "-RTN")

	if isRoundTrip {
		// Update BOTH legs
		baseReference := strings.TrimSuffix(strings.TrimSuffix(booking.SessionID, "-OUT"), "-RTN")

		// updates := make(map[string]interface{})

		if req.FlightNumber != "" {
			// Update outbound flight
			config.DB.Model(&models.BookingSchedule{}).
				Where("session_id = ?", baseReference+"-OUT").
				Update("flight_number", req.FlightNumber)
		}
		if req.ScheduledAt != "" {
			scheduledTime, err := time.Parse(time.RFC3339, req.ScheduledAt)
			if err != nil || scheduledTime.Before(time.Now().Add(55*time.Minute)) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "scheduledAt must be at least 55 minutes in the future"})
				return
			}
			config.DB.Model(&models.BookingSchedule{}).
				Where("session_id = ?", baseReference+"-OUT").
				Update("scheduled_at", scheduledTime)
		}
		if req.ReturnFlightNumber != "" {
			config.DB.Model(&models.BookingSchedule{}).
				Where("session_id = ?", baseReference+"-RTN").
				Update("flight_number", req.ReturnFlightNumber)
		}
		if req.ReturnScheduledAt != "" {
			returnTime, err := time.Parse(time.RFC3339, req.ReturnScheduledAt)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid returnScheduledAt"})
				return
			}
			config.DB.Model(&models.BookingSchedule{}).
				Where("session_id = ?", baseReference+"-RTN").
				Update("scheduled_at", returnTime)
		}

		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "booking updated"})
		return
	}

	// Single leg update (existing logic)
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

	// Clean up the reference
	reference := strings.TrimSpace(strings.ToUpper(req.Reference))

	// Reject lookups with internal suffixes
	if strings.HasSuffix(reference, "-OUT") || strings.HasSuffix(reference, "-RTN") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid booking reference. Please use the reference from your confirmation email.",
		})
		return
	}

	// Find the booking (handle round trips)
	var booking models.BookingSchedule
	err := config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
		reference, req.Contact, req.Contact).First(&booking).Error

	if err != nil {
		// Try round trip outbound
		err = config.DB.Where("session_id = ? AND (LOWER(guest_email) = LOWER(?) OR RIGHT(guest_phone, 4) = ?)",
			reference+"-OUT", req.Contact, req.Contact).First(&booking).Error

		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
	}

	// Check if round trip
	if strings.Contains(booking.SessionID, "-OUT") {
		baseReference := strings.TrimSuffix(booking.SessionID, "-OUT")

		// Cancel both legs
		config.DB.Model(&models.BookingSchedule{}).
			Where("session_id IN ?", []string{baseReference + "-OUT", baseReference + "-RTN"}).
			Update("status", "cancelled")

		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "booking cancelled"})
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
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// ── Check Redis cache first ──
	cacheKey := "booking_payment:" + req.Reference
	ctx := context.Background()

	if config.RedisClient != nil {
		cached, err := config.RedisClient.Get(ctx, cacheKey).Result()
		if err == nil {
			// Cache hit! Return cached response
			var cachedResponse map[string]interface{}
			if json.Unmarshal([]byte(cached), &cachedResponse) == nil {
				c.JSON(http.StatusOK, cachedResponse)
				return
			}
		}
	}

	// Check current booking status first
	var booking models.BookingSchedule
	if err := config.DB.Where("session_id = ?", req.Reference).First(&booking).Error; err != nil {
		// If not found with exact reference, check if it's a round trip parent reference
		var outboundBooking models.BookingSchedule
		if err := config.DB.Where("session_id = ?", req.Reference+"-OUT").First(&outboundBooking).Error; err == nil {
			booking = outboundBooking
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
	}

	// Check if this is a round trip leg
	isRoundTripLeg := strings.Contains(booking.SessionID, "-OUT") || strings.Contains(booking.SessionID, "-RTN")

	// For round trips, check if any leg is already paid
	if isRoundTripLeg {
		baseReference := strings.TrimSuffix(strings.TrimSuffix(booking.SessionID, "-OUT"), "-RTN")
		var outboundCheck models.BookingSchedule
		var returnCheck models.BookingSchedule

		config.DB.Where("session_id = ?", baseReference+"-OUT").First(&outboundCheck)
		config.DB.Where("session_id = ?", baseReference+"-RTN").First(&returnCheck)

		if outboundCheck.PaymentStatus == "paid" && returnCheck.PaymentStatus == "paid" {
			// Build combined response HERE (with the check variables)
			combinedBooking := bookingWithDriverDetails(outboundCheck)

			// Add return leg info
			combinedBooking["return_flight_number"] = returnCheck.FlightNumber
			combinedBooking["return_scheduled_at"] = returnCheck.ScheduledAt
			combinedBooking["return_pickup_address"] = returnCheck.PickupAddress
			combinedBooking["return_pickup_lat"] = returnCheck.PickupLat
			combinedBooking["return_pickup_lng"] = returnCheck.PickupLng
			combinedBooking["return_dropoff_address"] = returnCheck.DropoffAddress
			combinedBooking["return_dropoff_lat"] = returnCheck.DropoffLat
			combinedBooking["return_dropoff_lng"] = returnCheck.DropoffLng
			combinedBooking["fare_total"] = outboundCheck.FareTotal + returnCheck.FareTotal
			combinedBooking["trip_type"] = "both"

			response := gin.H{
				"status":        "paid",
				"paymentStatus": "paid",
				"bookingStatus": "confirmed",
				"booking":       combinedBooking,
			}

			// Cache this successful response
			cacheSuccessfulResponse(cacheKey, response)

			c.JSON(http.StatusOK, response)
			return
		}
	} else {
		// Single leg - check if already paid
		if booking.PaymentStatus == "paid" {
			response := gin.H{
				"status":        "paid",
				"paymentStatus": booking.PaymentStatus,
				"bookingStatus": booking.Status,
				"booking":       bookingWithDriverDetails(booking),
			}

			// Cache this successful response
			cacheSuccessfulResponse(cacheKey, response)

			c.JSON(http.StatusOK, response)
			return
		}
	}

	// ── Resolve the payment intent (server-side source of truth) ──
	groupRef := strings.TrimSuffix(strings.TrimSuffix(booking.SessionID, "-OUT"), "-RTN")

	var intent models.PaymentIntent
	if err := config.DB.Where("reference = ? AND provider = ? AND status = 'pending'", groupRef, req.Provider).
		Order("attempt DESC").First(&intent).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no pending payment for this booking"})
		return
	}

	var success bool
	var paidAmount float64
	var err error

	switch req.Provider {
	case "paystack":
		success, _, paidAmount, err = PaystackService.VerifyTransaction(intent.ProviderRef)
	case "stripe":
		success, paidAmount, err = StripeService.VerifySession(intent.ProviderRef)
	}

	if err != nil {
		slog.Error("verification failed", "ref", groupRef, "provider_ref", intent.ProviderRef, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "verification failed"})
		return
	}

	if success && paidAmount+0.01 < intent.ExpectedAmount {
		slog.Error("underpayment detected",
			"ref", groupRef,
			"paid", paidAmount,
			"expected", intent.ExpectedAmount,
			"currency", intent.Currency)
		c.JSON(http.StatusBadRequest, gin.H{"error": "payment amount mismatch"})
		return
	}
	if err := confirmPayment(intent, req.Provider); err != nil {
		slog.Error("confirm payment failed", "ref", groupRef, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to confirm payment"})
		return
	}

	// Build the response from fresh rows
	var response gin.H
	if isRoundTripLeg {
		var freshOut, freshRtn models.BookingSchedule
		config.DB.Where("session_id = ?", groupRef+"-OUT").First(&freshOut)
		config.DB.Where("session_id = ?", groupRef+"-RTN").First(&freshRtn)

		combined := bookingWithDriverDetails(freshOut)
		combined["return_flight_number"] = freshRtn.FlightNumber
		combined["return_scheduled_at"] = freshRtn.ScheduledAt
		combined["return_pickup_address"] = freshRtn.PickupAddress
		combined["return_pickup_lat"] = freshRtn.PickupLat
		combined["return_pickup_lng"] = freshRtn.PickupLng
		combined["return_dropoff_address"] = freshRtn.DropoffAddress
		combined["return_dropoff_lat"] = freshRtn.DropoffLat
		combined["return_dropoff_lng"] = freshRtn.DropoffLng
		combined["fare_total"] = freshOut.FareTotal + freshRtn.FareTotal
		combined["trip_type"] = "both"

		response = gin.H{
			"status": "paid", "paymentStatus": "paid",
			"bookingStatus": "confirmed", "booking": combined,
		}
	} else {
		var fresh models.BookingSchedule
		config.DB.Where("id = ?", booking.ID).First(&fresh)
		response = gin.H{
			"status": "paid", "paymentStatus": "paid",
			"bookingStatus": "confirmed", "booking": bookingWithDriverDetails(fresh),
		}
	}

	cacheSuccessfulResponse(cacheKey, response)
	c.JSON(http.StatusOK, response)
}

// Helper function to cache successful payment responses
func cacheSuccessfulResponse(cacheKey string, response gin.H) {
	if config.RedisClient == nil {
		return
	}

	ctx := context.Background()
	data, err := json.Marshal(response)
	if err != nil {
		return
	}

	// Cache for 24 hours (payment status won't change after success)
	config.RedisClient.Set(ctx, cacheKey, data, 24*time.Hour)
}

type CreateRentalRequest struct {
	CarID            string  `json:"carId" binding:"required"`
	GuestName        string  `json:"guestName" binding:"required,min=2"`
	GuestPhone       string  `json:"guestPhone" binding:"required,min=9"`
	GuestEmail       string  `json:"guestEmail" binding:"required,email"`
	PickupDate       string  `json:"pickupDate" binding:"required"` // ISO-8601
	ReturnDate       string  `json:"returnDate" binding:"required"` // ISO-8601
	CollectionMethod string  `json:"collectionMethod" binding:"required,oneof=hub_pickup delivery"`
	DeliveryAddress  string  `json:"deliveryAddress"`                                        // Required if delivery
	PaymentMethod    string  `json:"paymentMethod" binding:"required,oneof=paystack stripe"` // paystack, stripe
	FareTotal        float64 `json:"fareTotal" binding:"required"`                           // From frontend
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
		SessionID:         reference,
		ServiceType:       "rental",
		Channel:           "direct",
		GuestName:         req.GuestName,
		GuestPhone:        req.GuestPhone,
		GuestEmail:        req.GuestEmail,
		TripType:          "rental",
		TierID:            car.ID, // Store rental car ID here
		RentalDays:        rentalDays,
		DeliveryOption:    req.CollectionMethod,
		ProcessingFee:     0, // ADD THIS
		FareTotal:         fareTotal,
		PaymentMode:       req.PaymentMethod,
		PaymentStatus:     "pending",
		Status:            "pending",
		ScheduledAt:       &pickupTime,
		ReturnScheduledAt: &returnTime,
		PickupAddress:     pickupAddress,
		DropoffAddress:    "Axis Hub", // return to hub
	}

	if err := config.DB.Create(&booking).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create rental booking"})
		return
	}

	res, err := createPaymentIntent(
		reference,
		req.PaymentMethod,
		req.GuestEmail,
		"Axis Rental - "+reference,
		fareTotal,
		os.Getenv("APP_URL_MAIN")+"/rentals?ref="+reference+"&provider="+req.PaymentMethod,
		os.Getenv("APP_URL_MAIN")+"/rentals?cancelled=1",
		map[string]interface{}{
			"type":       "rental_payment",
			"booking_id": booking.ID,
		},
	)
	if err != nil {
		config.DB.Delete(&booking)
		slog.Error("payment intent failed", "ref", reference, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate payment link"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"status": "success",
		"data": gin.H{
			"id":                booking.ID,
			"reference":         reference,
			"paymentUrl":        res.PaymentURL,
			"provider":          req.PaymentMethod,
			"stripe_session_id": res.StripeSessionID,
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

func getBookingType(booking models.BookingSchedule) string {
	switch booking.ServiceType {
	case "rental":
		return "rental"
	case "ride":
		if booking.ScheduledAt != nil {
			return "scheduled_ride"
		}
		return "quick_ride"
	default:
		return "quick_ride"
	}
}

// Add this function at the bottom of your booking.go file
func getDriverDetails(driverID string) gin.H {
	if driverID == "" {
		return nil
	}

	var driver struct {
		ID        string `db:"id" json:"id"`
		FirstName string `db:"first_name" json:"first_name"`
		LastName  string `db:"last_name" json:"last_name"`
		Phone     string `db:"phone" json:"phone"`
		Email     string `db:"email" json:"email"`
		AvatarURL string `db:"avatar_url" json:"avatar_url"` // Changed from avatar to avatar_url
	}

	// Get driver basic info with correct column names
	err := config.DB.Raw(`
		SELECT id, 
		       COALESCE(first_name, '') as first_name, 
		       COALESCE(last_name, '') as last_name,
		       COALESCE(phone, '') as phone,
		       COALESCE(email, '') as email,
		       COALESCE(avatar_url, '') as avatar_url
		FROM drivers WHERE id = ?
	`, driverID).Scan(&driver).Error

	if err != nil {
		slog.Error("Failed to fetch driver", "error", err, "driver_id", driverID)
		return nil
	}

	if driver.ID == "" {
		slog.Warn("Driver not found", "driver_id", driverID)
		return nil
	}

	slog.Info("Driver found", "driver", driver)

	// Get driver's vehicle info with correct column names
	var vehicle struct {
		CarMake     string `db:"car_make" json:"car_make"`
		CarModel    string `db:"car_model" json:"car_model"`
		Year        string `db:"year" json:"year"`
		Color       string `db:"color" json:"color"`
		PlateNumber string `db:"plate_number" json:"plate_number"`
		ImageURL    string `db:"image_url" json:"image_url"`
	}

	err = config.DB.Raw(`
		SELECT COALESCE(car_make, '') as car_make,
		       COALESCE(car_model, '') as car_model,
		       COALESCE(year, '') as year,
		       COALESCE(color, '') as color,
		       COALESCE(plate_number, '') as plate_number,
		       COALESCE(image_url, '') as image_url
		FROM driver_vehicles 
		WHERE driver_id = ? AND is_active = 1
		LIMIT 1
	`, driverID).Scan(&vehicle).Error

	if err != nil {
		slog.Warn("Failed to fetch vehicle", "error", err)
	}

	result := gin.H{
		"id":         driver.ID,
		"first_name": driver.FirstName,
		"last_name":  driver.LastName,
		"phone":      driver.Phone,
		"email":      driver.Email,
		"avatar_url": driver.AvatarURL,
	}

	// Add vehicle info if found
	if vehicle.CarMake != "" || vehicle.CarModel != "" || vehicle.PlateNumber != "" {
		result["vehicle"] = gin.H{
			"car_make":     vehicle.CarMake,
			"car_model":    vehicle.CarModel,
			"year":         vehicle.Year,
			"color":        vehicle.Color,
			"plate_number": vehicle.PlateNumber,
			"image_url":    vehicle.ImageURL,
		}
	}

	return result
}

func bookingWithDriverDetails(booking models.BookingSchedule) gin.H {
	sessionID := booking.SessionID
	if strings.HasSuffix(sessionID, "-OUT") {
		sessionID = strings.TrimSuffix(sessionID, "-OUT")
	} else if strings.HasSuffix(sessionID, "-RTN") {
		sessionID = strings.TrimSuffix(sessionID, "-RTN")
	}
	bookingJSON := gin.H{
		"id":                     booking.ID,
		"session_id":             sessionID,
		"driver_id":              booking.DriverID,
		"driver_status":          booking.DriverStatus,
		"service_type":           booking.ServiceType,
		"channel":                booking.Channel,
		"guest_name":             booking.GuestName,
		"guest_phone":            booking.GuestPhone,
		"guest_email":            booking.GuestEmail,
		"trip_type":              booking.TripType,
		"airport":                booking.Airport,
		"track_flight":           booking.TrackFlight,
		"flight_number":          booking.FlightNumber,
		"scheduled_at":           booking.ScheduledAt,
		"return_flight_number":   booking.ReturnFlightNumber,
		"return_scheduled_at":    booking.ReturnScheduledAt,
		"pickup_address":         booking.PickupAddress,
		"dropoff_address":        booking.DropoffAddress,
		"pickup_lat":             booking.PickupLat,
		"pickup_lng":             booking.PickupLng,
		"dropoff_lat":            booking.DropoffLat,
		"dropoff_lng":            booking.DropoffLng,
		"return_pickup_address":  booking.ReturnPickupAddress,
		"return_pickup_lat":      booking.ReturnPickupLat,
		"return_pickup_lng":      booking.ReturnPickupLng,
		"return_dropoff_address": booking.ReturnDropoffAddress,
		"return_dropoff_lat":     booking.ReturnDropoffLat,
		"return_dropoff_lng":     booking.ReturnDropoffLng,
		"distance_km":            booking.DistanceKm,
		"fare_total":             booking.FareTotal,
		"passengers":             booking.Passengers,
		"luggage":                booking.Luggage,
		"tier_id":                booking.TierID,
		"payment_mode":           booking.PaymentMode,
		"payment_status":         booking.PaymentStatus,
		"status":                 booking.Status,
		"notes":                  booking.Notes,
		"created_at":             booking.CreatedAt,
		"updated_at":             booking.UpdatedAt,
		"processing_fee":         booking.ProcessingFee,
		"protocol_fee":           booking.ProtocolFee,
		"driver":                 getDriverDetails(booking.DriverID),
	}

	//log driver details
	return bookingJSON
}

// After successful payment verification:
func SendBookingConfirmation(booking *models.BookingSchedule, emailService *services.EmailService) {
	if emailService != nil {
		// Get driver details
		driverName := ""
		driverPhone := ""
		carModel := ""
		plateNumber := ""

		if booking.DriverID != "" {
			driverDetails := getDriverDetails(booking.DriverID)
			if driverDetails != nil {
				if v, ok := driverDetails["first_name"].(string); ok {
					driverName = v
				}
				if v, ok := driverDetails["phone"].(string); ok {
					driverPhone = v
				}
				if vehicle, ok := driverDetails["vehicle"].(gin.H); ok {
					if v, ok := vehicle["car_model"].(string); ok {
						carModel = v
					}
					if v, ok := vehicle["plate_number"].(string); ok {
						plateNumber = v
					}
				}
			}
		}

		emailService.SendGuestBookingConfirmation(
			booking.GuestEmail,
			booking.GuestName,
			booking.SessionID,
			booking.TripType,
			booking.Airport,
			booking.PickupAddress,
			booking.DropoffAddress,
			booking.ScheduledAt.Format("Mon, Jan 2 at 3:04 PM"),
			"",
			booking.FlightNumber,
			booking.ReturnFlightNumber,
			fmt.Sprintf("%d", booking.Passengers),
			fmt.Sprintf("%d", booking.Luggage),
			booking.TierID,
			fmt.Sprintf("%.2f", booking.FareTotal),
			booking.PaymentMode,
			driverName,
			driverPhone,
			carModel,
			plateNumber,
			booking.TripType == "both",
			booking.DriverID != "",
		)
	}
}

func SendRoundTripBookingConfirmation(outbound, returnBooking *models.BookingSchedule, emailService *services.EmailService) {
	if emailService == nil {
		return
	}

	// Get driver details from outbound
	driverName := ""
	driverPhone := ""
	carModel := ""
	plateNumber := ""

	if outbound.DriverID != "" {
		driverDetails := getDriverDetails(outbound.DriverID)
		if driverDetails != nil {
			if v, ok := driverDetails["first_name"].(string); ok {
				driverName = v
			}
			if v, ok := driverDetails["phone"].(string); ok {
				driverPhone = v
			}
			if vehicle, ok := driverDetails["vehicle"].(gin.H); ok {
				if v, ok := vehicle["car_model"].(string); ok {
					carModel = v
				}
				if v, ok := vehicle["plate_number"].(string); ok {
					plateNumber = v
				}
			}
		}
	}

	// Clean parent reference
	parentRef := strings.TrimSuffix(outbound.SessionID, "-OUT")

	emailService.SendGuestBookingConfirmation(
		outbound.GuestEmail,
		outbound.GuestName,
		parentRef, // Use parent reference without -OUT
		"both",
		outbound.Airport,
		outbound.PickupAddress,
		outbound.DropoffAddress,
		outbound.ScheduledAt.Format("Mon, Jan 2 at 3:04 PM"),
		returnBooking.ScheduledAt.Format("Mon, Jan 2 at 3:04 PM"), // Return time
		outbound.FlightNumber,
		returnBooking.FlightNumber, // Return flight
		fmt.Sprintf("%d", outbound.Passengers),
		fmt.Sprintf("%d", outbound.Luggage),
		outbound.TierID,
		fmt.Sprintf("%.2f", outbound.FareTotal+returnBooking.FareTotal), // Combined fare
		outbound.PaymentMode,
		driverName,
		driverPhone,
		carModel,
		plateNumber,
		true, // Has return
		outbound.DriverID != "",
	)
}

type fareInput struct {
	AirportID            string
	TripType             string
	MainLat, MainLng     float64
	ReturnLat, ReturnLng float64
	Passengers           int
	Protocol             bool
}

type fareResult struct {
	Total       float64
	BaseFare    float64
	ExtrasTotal float64
	PlatformFee float64
	ProtocolFee float64
	DistanceKm  float64
}

func computeFare(in fareInput) (fareResult, error) {
	var airport struct{ Lat, Lng float64 }
	if err := config.DB.Raw(`SELECT lat, lng FROM airports WHERE id = ? AND is_active = 1`, in.AirportID).Scan(&airport).Error; err != nil {
		return fareResult{}, err
	}
	if airport.Lat == 0 && airport.Lng == 0 {
		return fareResult{}, fmt.Errorf("invalid airport ID")
	}

	var cfg struct {
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
		WHERE config_key IN ('base_fare','price_per_km','minimum_fare')
	`).Scan(&cfg)

	if cfg.PricePerKm == 0 {
		return fareResult{}, fmt.Errorf("fare config missing")
	}

	if math.Abs(in.MainLat) > 90 || math.Abs(in.MainLng) > 180 ||
		math.Abs(in.ReturnLat) > 90 || math.Abs(in.ReturnLng) > 180 {
		return fareResult{}, fmt.Errorf("invalid coordinates")
	}

	const maxLegKm = 800.0
	var distanceKm float64
	switch in.TripType {
	case "pickup":
		distanceKm = haversine(airport.Lat, airport.Lng, in.MainLat, in.MainLng)
		if distanceKm > maxLegKm {
			return fareResult{}, fmt.Errorf("distance %.1fkm exceeds service area", distanceKm)
		}
	case "dropoff":
		distanceKm = haversine(in.MainLat, in.MainLng, airport.Lat, airport.Lng)
		if distanceKm > maxLegKm {
			return fareResult{}, fmt.Errorf("distance %.1fkm exceeds service area", distanceKm)
		}
	case "both":
		leg1 := haversine(airport.Lat, airport.Lng, in.MainLat, in.MainLng)
		rLat, rLng := in.MainLat, in.MainLng
		if in.ReturnLat != 0 && in.ReturnLng != 0 {
			rLat, rLng = in.ReturnLat, in.ReturnLng
		}
		leg2 := haversine(rLat, rLng, airport.Lat, airport.Lng)
		if leg1 > maxLegKm || leg2 > maxLegKm {
			return fareResult{}, fmt.Errorf("distance exceeds service area")
		}
		distanceKm = leg1 + leg2
	default:
		return fareResult{}, fmt.Errorf("invalid trip type")
	}

	distanceKm = math.Round(distanceKm*10) / 10

	const platformFee = 28.0
	protocolFee := 0.0
	if in.Protocol {
		protocolFee = float64(in.Passengers) * 500
	}
	extras := platformFee + protocolFee

	base := cfg.BaseFare + (distanceKm * cfg.PricePerKm)
	if base < cfg.MinimumFare {
		base = cfg.MinimumFare
	}
	base = math.Round(base*100) / 100

	return fareResult{
		Total:       math.Round(base + extras), // must match estimate rounding
		BaseFare:    base,
		ExtrasTotal: extras,
		PlatformFee: platformFee,
		ProtocolFee: protocolFee,
		DistanceKm:  distanceKm,
	}, nil
}

func computeRentalFare(carID, collectionMethod string, pickup, ret time.Time) (float64, int, error) {
	var car models.RentalCar
	if err := config.DB.Where("id = ? AND is_active = 1", carID).First(&car).Error; err != nil {
		return 0, 0, fmt.Errorf("invalid car")
	}
	days := int(math.Ceil(ret.Sub(pickup).Hours() / 24))
	total := car.RentPerDay * float64(days)
	if collectionMethod == "delivery" {
		var fee float64
		config.DB.Raw(`SELECT CAST(config_value AS DECIMAL(10,2)) FROM app_config WHERE config_key = 'delivery_fee'`).Scan(&fee)
		total += fee
	}
	return math.Round(total*100) / 100, days, nil
}

type intentResult struct {
	PaymentURL      string
	ProviderRef     string
	StripeSessionID string
}

func createPaymentIntent(groupRef, provider, email, description string, fareGHS float64, callbackURL, cancelURL string, metadata map[string]interface{}) (*intentResult, error) {

	var attempt int64
	config.DB.Model(&models.PaymentIntent{}).Where("reference = ?", groupRef).Count(&attempt)
	attempt++

	attemptRef := fmt.Sprintf("%s-P%d", groupRef, attempt)

	intent := models.PaymentIntent{
		Reference:    groupRef,
		Provider:     provider,
		Attempt:      int(attempt),
		FareTotalGHS: fareGHS,
		Status:       "pending",
	}
	out := &intentResult{}

	if provider == "paystack" {
		metadata["reference"] = groupRef
		resp, err := PaystackService.GeneratePaymentLink(services.PaymentLinkRequest{
			Amount:      fareGHS,
			Email:       email,
			Description: description,
			Reference:   attemptRef,
			Metadata:    metadata,
			CallbackURL: callbackURL,
		})
		if err != nil {
			return nil, err
		}
		out.PaymentURL = resp.PaymentURL
		out.ProviderRef = attemptRef
		intent.ProviderRef = attemptRef
		intent.ExpectedAmount = math.Round(fareGHS*100) / 100
		intent.Currency = "GHS"
	} else {
		strMeta := map[string]string{"reference": groupRef}
		resp, err := StripeService.GeneratePaymentLink(
			fareGHS, "USD", email, description, attemptRef,
			strMeta, callbackURL, cancelURL,
		)
		if err != nil {
			return nil, err
		}
		out.PaymentURL = resp.PaymentURL
		out.ProviderRef = resp.SessionID
		out.StripeSessionID = resp.SessionID
		intent.ProviderRef = resp.SessionID
		intent.ExpectedAmount = resp.ChargedAmount
		intent.Currency = resp.ChargedCurrency
	}

	if err := config.DB.Create(&intent).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// confirmPayment marks a booking (or both round-trip legs) paid, records the
// trip payment, and fires notifications. Safe to call twice — it no-ops if
// already paid.
func confirmPayment(intent models.PaymentIntent, provider string) error {
	groupRef := intent.Reference

	// Locate the booking(s)
	var isRoundTrip bool
	var single models.BookingSchedule
	if err := config.DB.Where("session_id = ?", groupRef).First(&single).Error; err != nil {
		var out models.BookingSchedule
		if err := config.DB.Where("session_id = ?", groupRef+"-OUT").First(&out).Error; err != nil {
			return fmt.Errorf("booking not found for %s", groupRef)
		}
		isRoundTrip = true
	}

	tx := config.DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	updates := map[string]interface{}{
		"payment_status": "paid",
		"status":         "confirmed",
		"driver_status":  "assigned",
	}

	if isRoundTrip {
		var outB, rtnB models.BookingSchedule
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Where("session_id = ?", groupRef+"-OUT").First(&outB).Error; err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Where("session_id = ?", groupRef+"-RTN").First(&rtnB).Error; err != nil {
			tx.Rollback()
			return err
		}

		if outB.PaymentStatus == "paid" && rtnB.PaymentStatus == "paid" {
			tx.Rollback()
			return nil // already done
		}

		if err := tx.Model(&models.BookingSchedule{}).
			Where("session_id IN ?", []string{groupRef + "-OUT", groupRef + "-RTN"}).
			Updates(updates).Error; err != nil {
			tx.Rollback()
			return err
		}

		if err := tx.Model(&models.PaymentIntent{}).
			Where("id = ?", intent.ID).Update("status", "paid").Error; err != nil {
			tx.Rollback()
			return err
		}

		var n int64
		tx.Model(&models.TripPayment{}).Where("group_ref = ?", groupRef).Count(&n)
		if n == 0 {
			var driverPtr *string
			if outB.DriverID != "" {
				driverPtr = &outB.DriverID
			}
			if err := tx.Create(&models.TripPayment{
				TripID:      outB.ID,
				DriverID:    driverPtr,
				Amount:      outB.FareTotal + rtnB.FareTotal,
				Fee:         outB.ProcessingFee + rtnB.ProcessingFee,
				Method:      provider,
				Reference:   groupRef,
				BookingType: "scheduled_ride",
				Status:      "completed",
				GroupRef:    groupRef,
			}).Error; err != nil {
				tx.Rollback()
				return err
			}
		}

		if err := tx.Commit().Error; err != nil {
			return err
		}

		go handlers.NotifyDriver(outB)
		go handlers.NotifyDriver(rtnB)
		go SendRoundTripBookingConfirmation(&outB, &rtnB, emailService)
		go handlers.NotifyAdminNewBooking(outB)
		return nil
	}

	// Single leg
	var b models.BookingSchedule
	if err := tx.Set("gorm:query_option", "FOR UPDATE").
		Where("id = ?", single.ID).First(&b).Error; err != nil {
		tx.Rollback()
		return err
	}
	if b.PaymentStatus == "paid" {
		tx.Rollback()
		return nil
	}

	if err := tx.Model(&models.BookingSchedule{}).
		Where("id = ?", b.ID).Updates(updates).Error; err != nil {
		tx.Rollback()
		return err
	}

	if err := tx.Model(&models.PaymentIntent{}).
		Where("id = ?", intent.ID).Update("status", "paid").Error; err != nil {
		tx.Rollback()
		return err
	}

	var n int64
	tx.Model(&models.TripPayment{}).
		Where("trip_id = ? AND status = 'completed'", b.ID).Count(&n)
	if n == 0 {
		var driverPtr *string
		if b.DriverID != "" {
			driverPtr = &b.DriverID
		}
		if err := tx.Create(&models.TripPayment{
			TripID:      b.ID,
			DriverID:    driverPtr,
			Amount:      b.FareTotal,
			Fee:         b.ProcessingFee,
			Method:      provider,
			Reference:   b.SessionID,
			Status:      "completed",
			BookingType: getBookingType(b),
			GroupRef:    b.SessionID,
		}).Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	if err := tx.Commit().Error; err != nil {
		return err
	}

	if b.Channel == "partner" {
		go calculateCommission(b.ID)
	}
	go handlers.NotifyDriver(b)
	go SendBookingConfirmation(&b, emailService)
	go handlers.NotifyAdminNewBooking(b)
	return nil
}
