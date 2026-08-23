package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"time"

	"goapi/config"
	"goapi/models"

	"github.com/Timothylock/go-signin-with-apple/apple"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)


func loadApplePrivateKey() (string, error) {
	b64 := os.Getenv("APPLE_PRIVATE_KEY_B64")
	if b64 == "" {
		return "", fmt.Errorf("APPLE_PRIVATE_KEY_B64 not set")
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("failed to decode apple private key: %w", err)
	}
	return string(decoded), nil
}

// POST /v1/riders/apple-login
func AppleLogin(c *gin.Context) {
	var req struct {
		IDToken string `json:"id_token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID token required"})
		return
	}

	keyContents, err := loadApplePrivateKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Server misconfiguration"})
		return
	}

	secret, err := apple.GenerateClientSecret(
		keyContents,
		os.Getenv("APPLE_TEAM_ID"),
		os.Getenv("APPLE_CLIENT_ID"), // your Services ID, e.g. com.axis.web
		os.Getenv("APPLE_KEY_ID"),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate client secret"})
		return
	}

	client := apple.New()
	var resp apple.ValidationResponse
	err = client.VerifyAppToken(context.Background(), apple.AppValidationTokenRequest{
		ClientID:     os.Getenv("APPLE_CLIENT_ID"),
		ClientSecret: secret,
		Code:         req.IDToken, // for the JS flow this can also be an id_token; see note below
	}, &resp)
	if err != nil || resp.Error != "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid Apple token"})
		return
	}

	claim, err := apple.GetClaims(resp.IDToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid Apple identity token"})
		return
	}
	claims := *claim

	email, _ := claims["email"].(string)
	appleSub, _ := claims["sub"].(string)
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No email returned by Apple"})
		return
	}

	var rider models.Rider
	err = config.DB.Where("email = ?", email).First(&rider).Error
	if err != nil {
		rider = models.Rider{
			ID:         uuid.New().String(),
			Email:      email,
			AppleID:    appleSub,
			IsVerified: true,
		}
		if err := config.DB.Create(&rider).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not create rider"})
			return
		}
	} else if rider.AppleID == "" {
		rider.AppleID = appleSub
		config.DB.Save(&rider)
	}

	now := time.Now()
	rider.LastLoginAt = &now
	config.DB.Save(&rider)

	tokenString := generateRiderJWT(rider.ID, rider.Email)

	c.JSON(http.StatusOK, gin.H{
		"rider_id":     rider.ID,
		"email":        rider.Email,
		"first_name":   rider.FirstName,
		"access_token": tokenString,
		"token_type":   "bearer",
		"is_new":       rider.CreatedAt == rider.UpdatedAt,
	})
}