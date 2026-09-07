package web

import (
	"math"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"time"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"goapi/config"
	"goapi/models"
	"goapi/services"
)

// Google JWKS response
type GoogleJWKS struct {
	Keys []GoogleJWK `json:"keys"`
}

type GoogleJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

var emailService *services.EmailService

func InitEmailService() {
	emailService = services.NewEmailService()
}

func ApplyHotel(c *gin.Context) {
	var req models.HotelApplication
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check if email already exists
	var existingCount int64
	config.DB.Model(&models.Hotel{}).Where("contact_email = ?", req.WorkEmail).Count(&existingCount)
	if existingCount > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "application already exists with this email"})
		return
	}

	hotel := models.Hotel{
		ID:                    uuid.New().String(),
		Name:                  req.PropertyName,
		ContactName:           req.ContactName,
		ContactEmail:          req.WorkEmail,
		ContactPhone:          req.ContactPhone,
		RoomsMonthlyTransfers: req.RoomsTransfers,
		PreferredModel:        req.PreferredModel,
		CommissionRate:        0.12, // default 12%
		Status:                "pending",
	}

	if err := config.DB.Create(&hotel).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to submit application"})
		return
	}
	//send 
	if emailService != nil {
		go func() {
			if err := emailService.SendHotelApplicationConfirmation(
				hotel.ContactEmail,
				hotel.Name,
				hotel.ContactName,
			); err != nil {
				log.Printf("Failed to send hotel confirmation email: %v", err)
			}
		}()

		go func() {
			if err := emailService.SendNewHotelApplicationNotification(
				hotel.Name,
				hotel.ContactName,
				hotel.ContactEmail,
				hotel.ContactPhone,
				hotel.RoomsMonthlyTransfers,
				hotel.PreferredModel,
			); err != nil {
				log.Printf("Failed to send admin notification email: %v", err)
			}
		}()
	}

	c.JSON(http.StatusCreated, gin.H{
		"status":  "success",
		"message": "Application received. Our team will review and contact you.",
	})
}


type HotelLoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

func LoginHotel(c *gin.Context) {
	var req HotelLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var member models.HotelMember
	if err := config.DB.Where("email = ? AND is_active = 1", req.Email).First(&member).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(member.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Check hotel status
	var hotel models.Hotel
	config.DB.Where("id = ?", member.HotelID).First(&hotel)
	
	if hotel.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "hotel not approved yet"})
		return
	}

	// Generate JWT
	token := generateHotelJWT(member.ID, member.HotelID, member.Role)

	// Check if first login
	isFirstLogin := member.FirstLoginAt == nil

	c.JSON(http.StatusOK, gin.H{
		"token":        token,
		"isFirstLogin": isFirstLogin,
		"member": gin.H{
			"id":      member.ID,
			"email":   member.Email,
			"role":    member.Role,
			"hotelId": member.HotelID,
		},
	})
}


func GoogleLoginHotel(c *gin.Context) {
	var req struct {
		IDToken string `json:"id_token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID token required"})
		return
	}

	// Verify token
	email, googleID, name, picture, err := verifyGoogleIDToken(req.IDToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid Google token"})
		return
	}
	

	// Find hotel member by email
	var member models.HotelMember
	err = config.DB.Where("email = ? AND is_active = 1", email).First(&member).Error

	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "No approved hotel account found for this email. Please apply first."})
		return
	}

	// Check hotel is active
	var hotel models.Hotel
	config.DB.Where("id = ?", member.HotelID).First(&hotel)
	log.Printf("Google login: email=%s, member.HotelID=%s, hotel.Status=%s", email, member.HotelID, hotel.Status)
	if hotel.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "hotel not approved yet"})
		return
	}

	// Update Google ID if not set
	if member.GoogleID == "" {
		config.DB.Model(&member).Update("google_id", googleID)
	}

	token := generateHotelJWT(member.ID, member.HotelID, member.Role)
	isFirstLogin := member.FirstLoginAt == nil

	c.JSON(http.StatusOK, gin.H{
		"token":        token,
		"isFirstLogin": isFirstLogin,
		"member": gin.H{
			"id":      member.ID,
			"email":   member.Email,
			"role":    member.Role,
			"hotelId": member.HotelID,
		},
		"google_name":    name,
		"google_picture": picture,
	})
}

func verifyGoogleIDToken(idToken string) (email, googleID, name, picture string, err error) {
	// Parse token without verification first
	token, _, err := new(jwt.Parser).ParseUnverified(idToken, jwt.MapClaims{})
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to parse token: %v", err)
	}

	kid, ok := token.Header["kid"].(string)
	if !ok {
		return "", "", "", "", fmt.Errorf("missing kid in token header")
	}

	// Fetch Google JWKS
	resp, err := http.Get("https://www.googleapis.com/oauth2/v3/certs")
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to fetch JWKS: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var jwks GoogleJWKS
	if err := json.Unmarshal(body, &jwks); err != nil {
		return "", "", "", "", fmt.Errorf("failed to parse JWKS: %v", err)
	}

	// Find matching key
	var matchingKey *rsa.PublicKey
	for _, key := range jwks.Keys {
		if key.Kid == kid {
			nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
			if err != nil {
				continue
			}
			eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
			if err != nil {
				continue
			}

			n := new(big.Int).SetBytes(nBytes)
			e := int(new(big.Int).SetBytes(eBytes).Int64())

			matchingKey = &rsa.PublicKey{
				N: n,
				E: e,
			}
			break
		}
	}

	if matchingKey == nil {
		return "", "", "", "", fmt.Errorf("no matching key found")
	}

	// Verify token
	parsedToken, err := jwt.Parse(idToken, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodRS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return matchingKey, nil
	})
	if err != nil {
		return "", "", "", "", fmt.Errorf("token verification failed: %v", err)
	}

	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok || !parsedToken.Valid {
		return "", "", "", "", fmt.Errorf("invalid claims")
	}

	// Extract fields
	email, _ = claims["email"].(string)
	googleID, _ = claims["sub"].(string)
	name, _ = claims["name"].(string)
	picture, _ = claims["picture"].(string)

	// Verify email
	emailVerified, _ := claims["email_verified"].(bool)
	if !emailVerified {
		return "", "", "", "", fmt.Errorf("email not verified")
	}

	// Verify audience (your Google Client ID)
	aud, _ := claims["aud"].(string)
	expectedAud := os.Getenv("GOOGLE_CLIENT_ID")
	if aud != expectedAud {
		return "", "", "", "", fmt.Errorf("invalid audience")
	}

	if email == "" || googleID == "" {
		return "", "", "", "", fmt.Errorf("missing email or sub")
	}

	return email, googleID, name, picture, nil
}


type HotelSetupRequest struct {
	PropertyName      string `json:"propertyName" binding:"required"`
	City              string `json:"city" binding:"required"`
	FrontDeskContact  string `json:"frontDeskContact" binding:"required"`
	ContactPhone      string `json:"contactPhone" binding:"required"`
	PayoutMethod      string `json:"payoutMethod" binding:"required,oneof=bank_transfer momo"`
	PayoutDestination string `json:"payoutDestination" binding:"required"`
	HouseAccount      bool   `json:"houseAccount"`
}

func SetupHotel(c *gin.Context) {
	hotelID := c.GetString("hotel_id") // from JWT middleware

	var req HotelSetupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{
		"name":                  req.PropertyName,
		"city":                  req.City,
		"front_desk_contact":    req.FrontDeskContact,
		"contact_phone":         req.ContactPhone,
		"payout_method":         req.PayoutMethod,
		"payout_destination":    req.PayoutDestination,
		"house_account_enabled": req.HouseAccount,
	}

	config.DB.Model(&models.Hotel{}).Where("id = ?", hotelID).Updates(updates)

	// Mark first login complete
	config.DB.Model(&models.HotelMember{}).Where("hotel_id = ?", hotelID).
		Update("first_login_at", time.Now())

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Property setup complete"})
}


func HotelDashboard(c *gin.Context) {
	hotelID := c.GetString("hotel_id")

	var hotel models.Hotel
	config.DB.Where("id = ?", hotelID).First(&hotel)

	// Stats
	var activeBookings int64
	config.DB.Model(&models.Booking{}).
		Where("hotel_id = ? AND status IN ('confirmed', 'in_progress')", hotelID).
		Count(&activeBookings)

	var completedBookings int64
	config.DB.Model(&models.Booking{}).
		Where("hotel_id = ? AND status = 'completed'", hotelID).
		Count(&completedBookings)

	var pendingCommission float64
	config.DB.Raw(`
		SELECT COALESCE(SUM(amount), 0) FROM commissions 
		WHERE hotel_id = ? AND status = 'pending'
	`, hotelID).Scan(&pendingCommission)

	var availableCommission float64
	config.DB.Raw(`
		SELECT COALESCE(SUM(amount), 0) FROM commissions 
		WHERE hotel_id = ? AND status = 'available'
	`, hotelID).Scan(&availableCommission)

	// Recent bookings
	var recentBookings []models.BookingSchedule
	config.DB.Where("hotel_id = ?", hotelID).
		Order("created_at DESC").
		Limit(5).
		Find(&recentBookings)

	c.JSON(http.StatusOK, gin.H{
		"activeBookings":     activeBookings,
		"completedBookings":  completedBookings,
		"pendingCommission":  pendingCommission,
		"availableCommission": availableCommission,
		"commissionRate":     hotel.CommissionRate,
		"recentBookings":     recentBookings,
	})
}



func BookForGuest(c *gin.Context) {
	hotelID := c.GetString("hotel_id")

	var req CreateBookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.Channel = "partner"
	req.HotelID = hotelID

	var hotel models.Hotel
	if err := config.DB.Where("id = ? AND status = 'active'", hotelID).First(&hotel).Error; err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "hotel not active"})
		return
	}

	var booking *models.BookingSchedule

	booking = createHotelBooking(req, hotelID, req.PaymentMethod, "pending", "pending")
	

	if booking == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create booking"})
		return
	}

	// Generate secure payment token
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)

	// Store token with expiry (24 hours)
	paymentToken := models.PartnerPaymentToken{
		ID:        uuid.New().String(),
		BookingID: booking.ID,
		Token:     token,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := config.DB.Create(&paymentToken).Error; err != nil {
		// Don't fail the whole booking if token creation fails
		log.Printf("Failed to create payment token: %v", err)
	}

	// Generate payment selection link with token
	paymentSelectionURL := os.Getenv("APP_URL_MAIN") + "/pay?token=" + token

	// Send email to guest with secure payment link
	if emailService != nil {
		go func() {
			err := emailService.SendPartnerPaymentLinkEmail(
				booking.GuestEmail,
				booking.GuestName,
				booking.SessionID,
				paymentSelectionURL,
				fmt.Sprintf("%.2f", booking.FareTotal),
			)
			if err != nil {
				log.Printf("Failed to send payment link email: %v", err)
			}
		}()
	}

	c.JSON(http.StatusCreated, gin.H{
		"status": "success",
		"data": gin.H{
			"id":                  booking.ID,
			"reference":           booking.SessionID,
			"paymentSelectionUrl": paymentSelectionURL,
			"status":              "pending",
			"paymentStatus":       "pending",
			"fareTotal":           req.FareTotal,
			"currency":            "GHS",
		},
	})
}

func createHotelBooking(req CreateBookingRequest, hotelID, paymentMode, paymentStatus, bookingStatus string) *models.BookingSchedule {

	// Validate airport exists + get coords
	var airportCoords struct {
		Lat float64
		Lng float64
	}
	if err := config.DB.Raw(`SELECT lat, lng FROM airports WHERE id = ? AND is_active = 1`, req.AirportID).Scan(&airportCoords).Error; err != nil {
		
		return nil
	}
	if airportCoords.Lat == 0 && airportCoords.Lng == 0 {
		return nil
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

	hotelIDPtr := &hotelID

	booking := models.BookingSchedule{
		SessionID:           reference,
		HotelID:             hotelIDPtr,
		Channel:             "partner",
		DriverID:            req.DriverID,
		ServiceType:         req.ServiceType,
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
		TrackFlight:         req.TrackFlight,
		FlightNumber:        req.FlightNumber,
		ScheduledAt:   parseTime(req.ScheduledAt),
		ReturnFlightNumber:  req.ReturnFlightNumber,
		ReturnScheduledAt: parseTimePtr(req.ReturnScheduledAt),
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
		return nil
	}
	return &booking
}

func createHotelRentalBooking(req CreateBookingRequest, hotelID, paymentMode, paymentStatus, bookingStatus string) *models.BookingSchedule {
	var car models.RentalCar
	if err := config.DB.Where("id = ? AND is_active = 1", req.TierID).First(&car).Error; err != nil {
		return nil
	}

	pickupAddress := "Axis Hub"
	if req.CollectionMethod == "delivery" {
		if req.DeliveryAddress == "" {
			return nil
		}
		pickupAddress = req.DeliveryAddress
	}

	reference := generateBookingReference()
	hotelIDPtr := &hotelID

	booking := models.BookingSchedule{
		SessionID:      reference,
		HotelID:        hotelIDPtr,
		Channel:        "partner",
		ServiceType:    "rental",
		GuestName:      req.GuestName,
		GuestPhone:     req.GuestPhone,
		GuestEmail:     req.GuestEmail,
		TripType:       "rental",
		TierID:         car.ID,
		RentalDays:     req.RentalDays,
		DeliveryOption: req.CollectionMethod,
		PickupAddress:  pickupAddress,
		DropoffAddress: "Axis Hub",
		ScheduledAt:    parseTime(req.ScheduledAt),
		ReturnScheduledAt: parseTimePtr(req.ReturnScheduledAt),
		FareTotal:      req.FareTotal,
		PaymentMode:    paymentMode,
		PaymentStatus:  paymentStatus,
		Status:         bookingStatus,
	}

	if err := config.DB.Create(&booking).Error; err != nil {
		return nil
	}
	return &booking
}

// In hotel.go - Add these types and function

type CreatePartnerRentalRequest struct {
	CarID            string  `json:"carId" binding:"required"`
	GuestName        string  `json:"guestName" binding:"required,min=2"`
	GuestPhone       string  `json:"guestPhone" binding:"required,min=9"`
	GuestEmail       string  `json:"guestEmail" binding:"required,email"`
	PickupDate       string  `json:"pickupDate" binding:"required"`
	ReturnDate       string  `json:"returnDate" binding:"required"`
	CollectionMethod string  `json:"collectionMethod" binding:"required,oneof=hub_pickup delivery"`
	DeliveryAddress  string  `json:"deliveryAddress"`
	PaymentMethod    string  `json:"paymentMethod" binding:"required"`
	FareTotal        float64 `json:"fareTotal" binding:"required"`
	RoomNumber       string  `json:"roomNumber"`
	PaymentMode      string  `json:"paymentMode"` // "guest_link" or "house_account"
}

func BookRentalForGuest(c *gin.Context) {
	hotelID := c.GetString("hotel_id")

	var req CreatePartnerRentalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate hotel is active
	var hotel models.Hotel
	if err := config.DB.Where("id = ? AND status = 'active'", hotelID).First(&hotel).Error; err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "hotel not active"})
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
	if req.CollectionMethod == "delivery" {
		if req.DeliveryAddress == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "deliveryAddress required for delivery"})
			return
		}
		fareTotal += deliveryFee
	}

	reference := generateBookingReference()

	pickupAddress := "Axis Hub"
	if req.CollectionMethod == "delivery" {
		pickupAddress = req.DeliveryAddress
	}

	hotelIDPtr := &hotelID

	booking := models.BookingSchedule{
		SessionID:         reference,
		HotelID:           hotelIDPtr,
		Channel:           "partner",
		ServiceType:       "rental",
		GuestName:         req.GuestName,
		GuestPhone:        req.GuestPhone,
		GuestEmail:        req.GuestEmail,
		RoomNumber:        req.RoomNumber,
		TripType:          "rental",
		TierID:            car.ID,
		RentalDays:        rentalDays,
		DeliveryOption:    req.CollectionMethod,
		FareTotal:         fareTotal,
		PaymentMode:       req.PaymentMethod,
		PaymentStatus:     "pending",
		Status:            "pending",
		ScheduledAt:       &pickupTime,
		ReturnScheduledAt: &returnTime,
		PickupAddress:     pickupAddress,
		DropoffAddress:    "Axis Hub",
	}

	// Handle house account
	if req.PaymentMode == "house_account" {
		booking.PaymentStatus = "unpaid"
		booking.Status = "confirmed"
		
		if err := config.DB.Create(&booking).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create rental booking"})
			return
		}
		
		c.JSON(http.StatusCreated, gin.H{
			"status": "success",
			"data": gin.H{
				"id":            booking.ID,
				"reference":     reference,
				"status":        "confirmed",
				"paymentStatus": "unpaid",
				"fareTotal":     fareTotal,
				"rentalDays":    rentalDays,
				"currency":      "GHS",
			},
		})
		return
	}

	// Create booking
	if err := config.DB.Create(&booking).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create rental booking"})
		return
	}

	// Generate secure payment token
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)

	paymentToken := models.PartnerPaymentToken{
		ID:        uuid.New().String(),
		BookingID: booking.ID,
		Token:     token,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := config.DB.Create(&paymentToken).Error; err != nil {
		log.Printf("Failed to create payment token: %v", err)
	}

	// Generate payment selection link
	paymentSelectionURL := os.Getenv("APP_URL_MAIN") + "/pay?token=" + token

	// Send email to guest
	if emailService != nil {
		go func() {
			err := emailService.SendPartnerPaymentLinkEmail(
				booking.GuestEmail,
				booking.GuestName,
				booking.SessionID,
				paymentSelectionURL,
				fmt.Sprintf("%.2f", booking.FareTotal),
			)
			if err != nil {
				log.Printf("Failed to send payment link email: %v", err)
			}
		}()
	}

	c.JSON(http.StatusCreated, gin.H{
		"status": "success",
		"data": gin.H{
			"id":                  booking.ID,
			"reference":           reference,
			"paymentSelectionUrl": paymentSelectionURL,
			"status":              "pending",
			"paymentStatus":       "pending",
			"fareTotal":           fareTotal,
			"rentalDays":          rentalDays,
			"currency":            "GHS",
		},
	})
}
// Create actual payment link with token validation
func CreateGuestPaymentLink(c *gin.Context) {
	var req struct {
		Token    string `json:"token" binding:"required"`
		Provider string `json:"provider" binding:"required,oneof=paystack stripe"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate token
	var paymentToken models.PartnerPaymentToken
	if err := config.DB.Where("token = ?", req.Token).First(&paymentToken).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "invalid payment link"})
		return
	}

	// Check expiry
	if time.Now().After(paymentToken.ExpiresAt) {
		c.JSON(http.StatusGone, gin.H{"error": "payment link has expired"})
		return
	}

	// Get booking
	var booking models.BookingSchedule
	if err := config.DB.First(&booking, paymentToken.BookingID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}

	if booking.PaymentStatus == "paid" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "booking already paid"})
		return
	}

	// ── Determine callback URL based on service type ──
	var callbackURL string
	if booking.ServiceType == "rental" {
		callbackURL = os.Getenv("APP_URL_MAIN") + "/rentals?ref=" + booking.SessionID + "&provider=" + req.Provider
	} else {
		callbackURL = os.Getenv("APP_URL_MAIN") + "/book?ref=" + booking.SessionID + "&provider=" + req.Provider
	}

	// Generate actual payment link
	var paymentURL string
	var stripeSessionID string

	if req.Provider == "paystack" {
		resp, err := PaystackService.GeneratePaymentLink(services.PaymentLinkRequest{
			Amount:      booking.FareTotal,
			Email:       booking.GuestEmail,
			Description: "Axis Booking - " + booking.SessionID,
			Reference:   booking.SessionID,
			Metadata: map[string]interface{}{
				"type":       "booking_payment",
				"booking_id": booking.ID,
				"reference":  booking.SessionID,
				"hotel_id":   booking.HotelID,
			},
			CallbackURL: callbackURL,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate payment link"})
			return
		}
		paymentURL = resp.PaymentURL
	} else {
		var cancelURL string
		if booking.ServiceType == "rental" {
			cancelURL = os.Getenv("APP_URL_MAIN") + "/rentals?cancelled=1"
		} else {
			cancelURL = os.Getenv("APP_URL_MAIN") + "/book?cancelled=1"
		}
		
		resp, err := StripeService.GeneratePaymentLink(
			booking.FareTotal,
			"USD",
			booking.GuestEmail,
			"Axis Booking - "+booking.SessionID,
			booking.SessionID,
			map[string]string{
				"booking_id": fmt.Sprintf("%d", booking.ID),
				"reference":  booking.SessionID,
			},
			callbackURL,
			cancelURL,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate payment link"})
			return
		}
		paymentURL = resp.PaymentURL
		stripeSessionID = resp.SessionID
	}

	c.JSON(http.StatusOK, gin.H{
		"status":           "success",
		"paymentUrl":       paymentURL,
		"provider":         req.Provider,
		"stripe_session_id": stripeSessionID,
	})
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func parseTimePtr(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}


func calculateCommission(bookingID uint) {
	var booking models.BookingSchedule
	config.DB.Where("id = ?", bookingID).First(&booking)

	if booking.Channel != "partner" || booking.HotelID == nil {
		return
	}

	var hotel models.Hotel
	config.DB.Where("id = ?", *booking.HotelID).First(&hotel)

	commissionAmount := booking.FareTotal * hotel.CommissionRate

	commission := models.Commission{
		ID:          uuid.New().String(),
		BookingID:   booking.ID,
		HotelID:     hotel.ID,
		Rate:        hotel.CommissionRate,
		BaseAmount:  booking.FareTotal,
		Amount:      commissionAmount,
		Status:      "pending",  
		AvailableAt: nil,        
	}

	config.DB.Create(&commission)
}

type RequestPayoutRequest struct {
	Amount          float64 `json:"amount" binding:"required"`
	Method          string  `json:"method" binding:"required,oneof=momo bank"`
	Destination     string  `json:"destination" binding:"required"`
	AccountName     string  `json:"account_name" binding:"required"`
	BankName        string  `json:"bank_name,omitempty"`     // For bank transfers
	Network         string  `json:"network,omitempty"`       // For momo (MTN, Vodafone, AT)
}

func RequestPayout(c *gin.Context) {
	hotelID := c.GetString("hotel_id")

	var req RequestPayoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate method-specific fields
	if req.Method == "bank" {
		if req.BankName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bank_name is required for bank transfers"})
			return
		}
	}
	if req.Method == "momo" {
		if req.Network == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "network is required for mobile money"})
			return
		}
	}

	var hotel models.Hotel
	config.DB.Where("id = ?", hotelID).First(&hotel)

	// Check available balance
	var availableBalance float64
	config.DB.Raw(`
		SELECT COALESCE(SUM(amount), 0) FROM commissions 
		WHERE hotel_id = ? AND status = 'available'
	`, hotelID).Scan(&availableBalance)

	if req.Amount > availableBalance {
		c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient available balance"})
		return
	}

	now := time.Now()
	payout := models.Payout{
		ID:           uuid.New().String(),
		HotelID:      hotelID,
		Amount:       req.Amount,
		Method:       req.Method,
		Destination:  req.Destination,
		AccountName:  req.AccountName,
		BankName:     req.BankName,
		Network:      req.Network,
		Status:       "requested",
		RequestedAt: &now,
	}

	config.DB.Create(&payout)

	// Mark commissions as "pending" (being withdrawn)
	config.DB.Exec(`
		UPDATE commissions SET status = 'pending' 
		WHERE hotel_id = ? AND status = 'available' 
		ORDER BY created_at ASC 
		LIMIT ?
	`, hotelID, int(req.Amount))

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Payout requested. Processing may take 24-48 hours.",
		"payout":  payout,
	})
}


// GET /v1/hotels/bookings
func ListHotelBookings(c *gin.Context) {
	hotelID := c.GetString("hotel_id")

	var bookings []models.BookingSchedule
	config.DB.Where("hotel_id = ?", hotelID).
		Order("created_at DESC").
		Find(&bookings)

	c.JSON(http.StatusOK, gin.H{
		"status":   "success",
		"bookings": bookings,
	})
}

// GET /v1/hotels/earnings
func HotelEarnings(c *gin.Context) {
	hotelID := c.GetString("hotel_id")
	
	var commissions []models.Commission
	config.DB.Where("hotel_id = ?", hotelID).Order("created_at DESC").Find(&commissions)

	var payouts []models.Payout
	config.DB.Where("hotel_id = ?", hotelID).Order("requested_at DESC").Find(&payouts)
	
	// Pending = paid but ride not completed yet
	var totalPending float64
	config.DB.Raw(`
		SELECT COALESCE(SUM(amount), 0) FROM commissions 
		WHERE hotel_id = ? AND status = 'pending'
	`, hotelID).Scan(&totalPending)

	// Available = ride completed, can withdraw
	var totalAvailable float64
	config.DB.Raw(`
		SELECT COALESCE(SUM(amount), 0) FROM commissions 
		WHERE hotel_id = ? AND status = 'available'
	`, hotelID).Scan(&totalAvailable)

	// Paid = already withdrawn
	var totalPaid float64
	config.DB.Raw(`
		SELECT COALESCE(SUM(amount), 0) FROM commissions 
		WHERE hotel_id = ? AND status = 'paid'
	`, hotelID).Scan(&totalPaid)

	c.JSON(http.StatusOK, gin.H{
		"status":         "success",
		"commissions":    commissions,
		"payouts":        payouts,
		"totalPending":   totalPending,
		"totalAvailable": totalAvailable,
		"totalPaid":      totalPaid,
	})
}

//admin
func ApproveHotel(c *gin.Context) {
	hotelID := c.Param("id")

	var hotel models.Hotel
	if err := config.DB.First(&hotel, hotelID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "hotel not found"})
		return
	}

	// Create staff member (no password — use Google or set later)
	member := models.HotelMember{
		ID:           uuid.New().String(),
		HotelID:      hotel.ID,
		Email:        hotel.ContactEmail,
		PasswordHash: "", // empty — Google login only, or set password later
		Role:         "admin",
		IsActive:     true,
	}

	if err := config.DB.Create(&member).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create staff"})
		return
	}

	config.DB.Model(&hotel).Update("status", "active")

	// Send approval email
	if emailService != nil {
		// go emailService.SendHotelApprovalEmail(hotel.ContactEmail, hotel.Name)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Hotel approved"})
}

// GET /v1/hotels/me
func HotelMe(c *gin.Context) {
	hotelID := c.GetString("hotel_id")
	memberID := c.GetString("member_id")

	if hotelID == "" || memberID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid session"})
		return
	}

	// Get member
	var member models.HotelMember
	if err := config.DB.Where("id = ? AND is_active = 1", memberID).First(&member).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "member not found"})
		return
	}

	// Get hotel
	var hotel models.Hotel
	if err := config.DB.Where("id = ?", hotelID).First(&hotel).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "hotel not found"})
		return
	}

	if hotel.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "hotel not approved yet"})
		return
	}

	// Check if setup completed
	isSetupComplete := hotel.City != "" && hotel.PayoutMethod != "" && hotel.PayoutDestination != ""

	c.JSON(http.StatusOK, gin.H{
		"member": gin.H{
			"id":        member.ID,
			"email":     member.Email,
			"role":      member.Role,
			"firstLogin": member.FirstLoginAt == nil,
		},
		"hotel": gin.H{
			"id":                   hotel.ID,
			"name":                 hotel.Name,
			"city":                 hotel.City,
			"contactName":          hotel.ContactName,
			"contactEmail":         hotel.ContactEmail,
			"contactPhone":         hotel.ContactPhone,
			"frontDeskContact":     hotel.FrontDeskContact,
			"roomsMonthlyTransfers": hotel.RoomsMonthlyTransfers,
			"preferredModel":       hotel.PreferredModel,
			"commissionRate":       hotel.CommissionRate,
			"houseAccountEnabled":  hotel.HouseAccountEnabled,
			"payoutMethod":         hotel.PayoutMethod,
			"payoutDestination":    hotel.PayoutDestination,
			"status":               hotel.Status,
			"isSetupComplete":      isSetupComplete,
		},
	})
}

// handlers/web/hotel.go — add near ListHotelBookings

type UpdatePartnerBookingStatusRequest struct {
	BookingID uint   `json:"bookingId" binding:"required"`
	Status    string `json:"status" binding:"required,oneof=confirmed en_route completed cancelled"`
}

func UpdatePartnerBookingStatus(c *gin.Context) {
	hotelID := c.GetString("hotel_id")

	var req UpdatePartnerBookingStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var booking models.BookingSchedule
	if err := config.DB.Where("id = ? AND hotel_id = ?", req.BookingID, hotelID).First(&booking).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}
	if booking.Status == "completed" || booking.Status == "cancelled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "booking already " + booking.Status})
		return
	}

	updates := map[string]interface{}{"status": req.Status}
	if req.Status == "completed" && booking.PaymentMode == "house_account" {
		updates["payment_status"] = "paid"
	}
	config.DB.Model(&booking).Updates(updates)

	if req.Status == "completed" {
		go calculateCommission(booking.ID)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}


// handlers/web/hotel.go

func GetGuestPaymentDetails(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token required"})
		return
	}

	var paymentToken models.PartnerPaymentToken
	if err := config.DB.Where("token = ?", token).First(&paymentToken).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "invalid payment link"})
		return
	}
	if time.Now().After(paymentToken.ExpiresAt) {
		c.JSON(http.StatusGone, gin.H{"error": "payment link has expired"})
		return
	}

	var booking models.BookingSchedule
	if err := config.DB.First(&booking, paymentToken.BookingID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"reference":   booking.SessionID,
			"guestName":   booking.GuestName,
			"fareTotal":   booking.FareTotal,
			"currency":    "GHS",
			"serviceType": booking.ServiceType,
			"tripType":    booking.TripType,
			"alreadyPaid": booking.PaymentStatus == "paid",
		},
	})
}

type PartnerRentalFields struct {
	CollectionMethod string `json:"collectionMethod"` // hub_pickup | delivery
	DeliveryAddress  string `json:"deliveryAddress"`
}


func generateHotelJWT(memberID, hotelID, role string) string {
	claims := jwt.MapClaims{
		"member_id": memberID,
		"hotel_id":  hotelID,
		"role":      role,
		"exp":       time.Now().Add(24 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, _ := token.SignedString([]byte(os.Getenv("JWT_SECRET")))
	return signedToken
}