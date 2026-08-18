// handlers/otp.go - Complete fixed version
package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"time"

	"goapi/config"
	"goapi/models"
	"goapi/services"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Email service
var emailService *services.EmailService

// OTP storage (in production, use Redis)
var otpStore = make(map[string]OTPData)

func InitEmailService() {
	emailService = services.NewEmailService()
}

type OTPData struct {
	Code      string
	ExpiresAt time.Time
}

func generateOTP() string {
	b := make([]byte, 3)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// POST /api/v1/riders/check-email
func CheckEmail(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Valid email is required"})
		return
	}

	var rider models.Rider
	err := config.DB.Where("email = ?", req.Email).First(&rider).Error

	// ALWAYS send OTP regardless of whether user exists or not
	otp := generateOTP()
	otpStore[req.Email] = OTPData{
		Code:      otp,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	// Send OTP via email
	if emailService != nil {
		if err := emailService.SendOTPEmail(req.Email, otp); err != nil {
			log.Printf("Failed to send OTP email: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send OTP"})
			return
		}
	}

	// Return whether user exists or not (for UI flow)
	if err != nil {
		// New user
		c.JSON(http.StatusOK, gin.H{
			"exists":       false,
			"requires_otp": true,
			"message":      "OTP sent to your email",
		})
	} else {
		// Existing user
		c.JSON(http.StatusOK, gin.H{
			"exists":       true,
			"rider_id":     rider.ID,
			"requires_otp": true,
			"message":      "OTP sent to your email",
		})
	}
}

// POST /api/v1/riders/verify-otp
func VerifyOTP(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
		OTP   string `json:"otp" binding:"required,len=6"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Email and valid OTP required"})
		return
	}

	// Verify OTP
	otpData, exists := otpStore[req.Email]
	if !exists || otpData.Code != req.OTP || time.Now().After(otpData.ExpiresAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or expired OTP"})
		return
	}

	// Clean up OTP
	delete(otpStore, req.Email)

	// Find or create rider
	var rider models.Rider
	err := config.DB.Where("email = ?", req.Email).First(&rider).Error
	if err != nil {
		// Create new rider
		rider = models.Rider{
			ID:         uuid.New().String(),
			Email:      req.Email,
			IsVerified: true,
		}
		if err := config.DB.Create(&rider).Error; err != nil {
			log.Printf("Failed to create rider: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not create rider"})
			return
		}
	} else {
		// Update existing rider verification status
		rider.IsVerified = true
		config.DB.Save(&rider)
	}

	// Generate JWT
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"rider_id": rider.ID,
		"email":    rider.Email,
		"exp":      time.Now().Add(30 * 24 * time.Hour).Unix(), // 30 days
	})

	tokenString, err := token.SignedString([]byte(os.Getenv("JWT_SECRET")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"rider_id":     rider.ID,
		"email":        rider.Email,
		"access_token": tokenString,
		"token_type":   "bearer",
		"is_new":       rider.CreatedAt == rider.UpdatedAt,
	})
}