package web

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"
	"os"
	"strings"
	"log/slog"

	"github.com/gin-gonic/gin"

	"goapi/config"
	"goapi/models"
	"goapi/services"
	"goapi/handlers"
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
	Protocol         bool    `json:"protocol"`
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
	
	// Protocol fee: GHS 500 per person if selected
	if req.Protocol {
		extrasTotal += float64(req.Passengers) * 500
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
	TrackFlight        bool    `json:"track_flight"`
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
	HotelID string `json:"hotelId"`
	PaymentMode   string `json:"paymentMode"`
	CollectionMethod string `json:"collectionMethod"`
	DeliveryAddress string `json:"deliveryAddress"`
	Protocol       bool    `json:"protocol"`
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

	protocolFee := 0.0
	if req.Protocol {
		protocolFee = float64(req.Passengers) * 500
	}


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
			ProtocolFee:   protocolFee,
			FareTotal:      outboundFare,
			PaymentMode:    req.PaymentMethod,
			PaymentStatus:  "pending",
			Status:         "pending",
			Notes:          req.Notes,
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
			ProtocolFee:   protocolFee,
			FareTotal:      returnFare,
			PaymentMode:    req.PaymentMethod,
			PaymentStatus:  "pending",
			Status:         "pending",
			Notes:          req.Notes,
		}

		if err := config.DB.Create(&returnBooking).Error; err != nil {
			// Rollback outbound if return fails
			config.DB.Delete(&outboundBooking)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create return booking"})
			return
		}

		// Generate payment link for total fare
		var paymentURL string
		var stripeSessionID string

		if req.PaymentMethod == "paystack" {
			resp, err := PaystackService.GeneratePaymentLink(services.PaymentLinkRequest{
				Amount:      req.FareTotal,
				Email:       req.GuestEmail,
				Description: "Axis Booking - " + reference,
				Reference:   reference,
				Metadata: map[string]interface{}{
					"type":              "booking_payment",
					"booking_id":        outboundBooking.ID,
					"return_booking_id": returnBooking.ID,
					"reference":         reference,
					"trip_type":         "both",
				},
				CallbackURL: os.Getenv("APP_URL_MAIN") + "/book?ref=" + reference + "&provider=paystack",
			})
			if err == nil {
				paymentURL = resp.PaymentURL
			} else {
				config.DB.Delete(&outboundBooking)
				config.DB.Delete(&returnBooking)
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
					"booking_id":        fmt.Sprintf("%d", outboundBooking.ID),
					"return_booking_id": fmt.Sprintf("%d", returnBooking.ID),
					"reference":         reference,
					"trip_type":         "both",
				},
				os.Getenv("APP_URL_MAIN") + "/book?ref=" + reference + "&provider=stripe",
				os.Getenv("APP_URL_MAIN") + "/book?cancelled=1",
			)
			if err == nil {
				paymentURL = resp.PaymentURL
				stripeSessionID = resp.SessionID
			} else {
				config.DB.Delete(&outboundBooking)
				config.DB.Delete(&returnBooking)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate payment link"})
				return
			}
		}

		c.JSON(http.StatusCreated, gin.H{
			"status": "success",
			"data": gin.H{
				"id":                outboundBooking.ID,
				"reference":         reference,
				"outboundReference": outboundBooking.SessionID,
				"returnReference":   returnBooking.SessionID,
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
			ProtocolFee:   protocolFee,
			FareTotal:      req.FareTotal,
			PaymentMode:    req.PaymentMethod,
			PaymentStatus:  "pending",
			Status:         "pending",
			Notes:          req.Notes,
		}

		if err := config.DB.Create(&booking).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create booking"})
			return
		}

		// Generate payment link
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
					"trip_type":  req.TripType,
				},
				CallbackURL: os.Getenv("APP_URL_MAIN") + "/book?ref=" + reference + "&provider=paystack",
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
					"trip_type":  req.TripType,
				},
				os.Getenv("APP_URL_MAIN") + "/book?ref=" + reference + "&provider=stripe",
				os.Getenv("APP_URL_MAIN") + "/book?cancelled=1",
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
		SessionID string `json:"sessionId"`
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
			"booking":       bookingWithDriverDetails(booking),
		})
		return
	}

	// Use a transaction with row locking to prevent duplicates
	tx := config.DB.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "transaction failed"})
		return
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var response gin.H

	if isRoundTripLeg {
		// ── Handle ROUND TRIP payment ──
		baseReference := strings.TrimSuffix(strings.TrimSuffix(booking.SessionID, "-OUT"), "-RTN")
		
		// Find and lock both legs
		var outboundBooking models.BookingSchedule
		var returnBooking models.BookingSchedule
		
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Where("session_id = ?", baseReference+"-OUT").
			First(&outboundBooking).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "outbound booking not found"})
			return
		}
		
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Where("session_id = ?", baseReference+"-RTN").
			First(&returnBooking).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "return booking not found"})
			return
		}

		// Double-check payment status after acquiring locks
		if outboundBooking.PaymentStatus == "paid" && returnBooking.PaymentStatus == "paid" {
			tx.Rollback()
			combinedBooking := bookingWithDriverDetails(outboundBooking)
			combinedBooking["return_flight_number"] = returnBooking.FlightNumber
			combinedBooking["return_scheduled_at"] = returnBooking.ScheduledAt
			combinedBooking["return_pickup_address"] = returnBooking.PickupAddress
			combinedBooking["return_pickup_lat"] = returnBooking.PickupLat
			combinedBooking["return_pickup_lng"] = returnBooking.PickupLng
			combinedBooking["return_dropoff_address"] = returnBooking.DropoffAddress
			combinedBooking["return_dropoff_lat"] = returnBooking.DropoffLat
			combinedBooking["return_dropoff_lng"] = returnBooking.DropoffLng
			combinedBooking["fare_total"] = outboundBooking.FareTotal + returnBooking.FareTotal
			combinedBooking["trip_type"] = "both"
			
			response = gin.H{
				"status":        "paid",
				"paymentStatus": "paid",
				"bookingStatus": "confirmed",
				"booking":       combinedBooking,
			}
			cacheSuccessfulResponse(cacheKey, response)
			c.JSON(http.StatusOK, response)
			return
		}

		// Update both legs
		updates := map[string]interface{}{
			"payment_status": "paid",
			"status":         "confirmed",
			"driver_status":  "assigned",
		}
		
		if err := tx.Model(&models.BookingSchedule{}).
			Where("session_id = ?", baseReference+"-OUT").
			Updates(updates).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update outbound booking"})
			return
		}
		
		if err := tx.Model(&models.BookingSchedule{}).
			Where("session_id = ?", baseReference+"-RTN").
			Updates(updates).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update return booking"})
			return
		}

		// Create ONE payment record for the total amount
		var existingPaymentCount int64
		tx.Model(&models.TripPayment{}).
			Where("group_ref = ?", baseReference).
			Count(&existingPaymentCount)
		
		if existingPaymentCount == 0 {
			// Total amount is the sum of both legs
			totalAmount := outboundBooking.FareTotal + returnBooking.FareTotal
			
			var driverIDPtr *string
			if outboundBooking.DriverID != "" {
				driverIDPtr = &outboundBooking.DriverID
			}
			
			paymentRecord := models.TripPayment{
				TripID:      outboundBooking.ID,
				DriverID:    driverIDPtr,
				Amount:      totalAmount,
				Fee:         totalAmount,
				Method:      req.Provider,
				Reference:   baseReference,
				BookingType: "scheduled_ride",
				Status:      "completed",
				GroupRef:    baseReference,
			}
			
			if err := tx.Create(&paymentRecord).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create payment record"})
				return
			}
		}

		// Commit transaction
		if err := tx.Commit().Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
			return
		}

		// Notify driver for both legs (outside transaction)
		go handlers.NotifyDriver(outboundBooking)
		go handlers.NotifyDriver(returnBooking)
		go SendRoundTripBookingConfirmation(&outboundBooking, &returnBooking, emailService)


		combinedBooking := bookingWithDriverDetails(outboundBooking)
		combinedBooking["return_flight_number"] = returnBooking.FlightNumber
		combinedBooking["return_scheduled_at"] = returnBooking.ScheduledAt
		combinedBooking["return_pickup_address"] = returnBooking.PickupAddress
		combinedBooking["return_pickup_lat"] = returnBooking.PickupLat
		combinedBooking["return_pickup_lng"] = returnBooking.PickupLng
		combinedBooking["return_dropoff_address"] = returnBooking.DropoffAddress
		combinedBooking["return_dropoff_lat"] = returnBooking.DropoffLat
		combinedBooking["return_dropoff_lng"] = returnBooking.DropoffLng
		combinedBooking["fare_total"] = outboundBooking.FareTotal + returnBooking.FareTotal
		combinedBooking["trip_type"] = "both"
		
		response = gin.H{
			"status":        "paid",
			"paymentStatus": "paid",
			"bookingStatus": "confirmed",
			"booking":       combinedBooking,
		}

	} else {
		// ── Handle SINGLE LEG payment ──
		
		// Lock the booking row to prevent concurrent updates
		var lockedBooking models.BookingSchedule
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Where("id = ?", booking.ID).
			First(&lockedBooking).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to lock booking"})
			return
		}

		// Double-check payment status after acquiring lock
		if lockedBooking.PaymentStatus == "paid" {
			tx.Rollback()
			response = gin.H{
				"status":        "paid",
				"paymentStatus": lockedBooking.PaymentStatus,
				"bookingStatus": lockedBooking.Status,
				"booking":       bookingWithDriverDetails(lockedBooking),
			}
			cacheSuccessfulResponse(cacheKey, response)
			c.JSON(http.StatusOK, response)
			return
		}

		// Check if trip_payment already exists
		var tripPaymentCount int64
		if err := tx.Model(&models.TripPayment{}).
			Where("trip_id = ? AND status = 'completed'", booking.ID).
			Count(&tripPaymentCount).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check payment record"})
			return
		}

		// Update booking status
		updates := map[string]interface{}{
			"payment_status": "paid",
			"status":         "confirmed",
			"driver_status":  "assigned",
		}
		if err := tx.Model(&models.BookingSchedule{}).
			Where("id = ?", booking.ID).
			Updates(updates).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update booking"})
			return
		}

		// Create trip_payment record only if it doesn't exist
		if tripPaymentCount == 0 {
			var driverIDPtr *string
			if booking.DriverID != "" {
				driverIDPtr = &booking.DriverID
			}
			
			paymentRecord := models.TripPayment{
				TripID:      booking.ID,
				DriverID:    driverIDPtr,
				Amount:      booking.FareTotal,
				Fee:         booking.FareTotal,
				Method:      req.Provider,
				Reference:   booking.SessionID,
				Status:      "completed",
				BookingType: getBookingType(booking),
				GroupRef:    booking.SessionID,
			}
			if err := tx.Create(&paymentRecord).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create payment record"})
				return
			}
		}

		// Commit transaction
		if err := tx.Commit().Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
			return
		}

		// Calculate commission for partner bookings (outside transaction)
		if booking.Channel == "partner" {
			go calculateCommission(booking.ID)
		}

		// Notify driver (outside transaction)
		go handlers.NotifyDriver(booking)
		go SendBookingConfirmation(&booking, emailService)


		response = gin.H{
			"status":        "paid",
			"paymentStatus": "paid",
			"bookingStatus": "confirmed",
			"booking":       bookingWithDriverDetails(booking),
		}
	}

	// Cache the successful response
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
			CallbackURL: os.Getenv("APP_URL_MAIN") + "/rentals?ref=" + reference + "&provider=paystack",
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
			os.Getenv("APP_URL_MAIN") + "/rentals?ref=" + reference + "&provider=stripe",
			os.Getenv("APP_URL_MAIN") + "/rentals?cancelled=1",
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
		"id":                 booking.ID,
		"session_id":         sessionID,
		"driver_id":          booking.DriverID,
		"driver_status":      booking.DriverStatus,
		"service_type":       booking.ServiceType,
		"channel":            booking.Channel,
		"guest_name":         booking.GuestName,
		"guest_phone":        booking.GuestPhone,
		"guest_email":        booking.GuestEmail,
		"trip_type":          booking.TripType,
		"airport":            booking.Airport,
		"track_flight":       booking.TrackFlight,
		"flight_number":      booking.FlightNumber,
		"scheduled_at":       booking.ScheduledAt,
		"return_flight_number": booking.ReturnFlightNumber,
		"return_scheduled_at":  booking.ReturnScheduledAt,
		"pickup_address":     booking.PickupAddress,
		"dropoff_address":    booking.DropoffAddress,
		"pickup_lat":         booking.PickupLat,
		"pickup_lng":         booking.PickupLng,
		"dropoff_lat":        booking.DropoffLat,
		"dropoff_lng":        booking.DropoffLng,
		"return_pickup_address":  booking.ReturnPickupAddress,
		"return_pickup_lat":      booking.ReturnPickupLat,
		"return_pickup_lng":      booking.ReturnPickupLng,
		"return_dropoff_address": booking.ReturnDropoffAddress,
		"return_dropoff_lat":     booking.ReturnDropoffLat,
		"return_dropoff_lng":     booking.ReturnDropoffLng,
		"distance_km":        booking.DistanceKm,
		"fare_total":         booking.FareTotal,
		"passengers":         booking.Passengers,
		"luggage":            booking.Luggage,
		"tier_id":            booking.TierID,
		"payment_mode":       booking.PaymentMode,
		"payment_status":     booking.PaymentStatus,
		"status":             booking.Status,
		"notes":              booking.Notes,
		"created_at":         booking.CreatedAt,
		"updated_at":         booking.UpdatedAt,
		"driver":             getDriverDetails(booking.DriverID),
	}
	

	//log driver details
	return bookingJSON
}

// After successful payment verification:
func SendBookingConfirmation(booking *models.BookingSchedule, emailService *services.EmailService) {	if emailService != nil {
		// Get driver details
		driverName := ""
		driverPhone := ""
		carModel := ""
		plateNumber := ""
		
		if booking.DriverID != "" {
			driverDetails := getDriverDetails(booking.DriverID)
			if driverDetails != nil {
				if v, ok := driverDetails["first_name"].(string); ok { driverName = v }
				if v, ok := driverDetails["phone"].(string); ok { driverPhone = v }
				if vehicle, ok := driverDetails["vehicle"].(gin.H); ok {
					if v, ok := vehicle["car_model"].(string); ok { carModel = v }
					if v, ok := vehicle["plate_number"].(string); ok { plateNumber = v }
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
			if v, ok := driverDetails["first_name"].(string); ok { driverName = v }
			if v, ok := driverDetails["phone"].(string); ok { driverPhone = v }
			if vehicle, ok := driverDetails["vehicle"].(gin.H); ok {
				if v, ok := vehicle["car_model"].(string); ok { carModel = v }
				if v, ok := vehicle["plate_number"].(string); ok { plateNumber = v }
			}
		}
	}
	
	// Clean parent reference
	parentRef := strings.TrimSuffix(outbound.SessionID, "-OUT")
	
	emailService.SendGuestBookingConfirmation(
		outbound.GuestEmail,
		outbound.GuestName,
		parentRef,  // Use parent reference without -OUT
		"both",
		outbound.Airport,
		outbound.PickupAddress,
		outbound.DropoffAddress,
		outbound.ScheduledAt.Format("Mon, Jan 2 at 3:04 PM"),
		returnBooking.ScheduledAt.Format("Mon, Jan 2 at 3:04 PM"),  // Return time
		outbound.FlightNumber,
		returnBooking.FlightNumber,  // Return flight
		fmt.Sprintf("%d", outbound.Passengers),
		fmt.Sprintf("%d", outbound.Luggage),
		outbound.TierID,
		fmt.Sprintf("%.2f", outbound.FareTotal + returnBooking.FareTotal),  // Combined fare
		outbound.PaymentMode,
		driverName,
		driverPhone,
		carModel,
		plateNumber,
		true,  // Has return
		outbound.DriverID != "",
	)
}