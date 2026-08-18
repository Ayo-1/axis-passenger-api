// handlers/google.go - Cleaned up version
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"goapi/config"
	"goapi/models"

	"firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"google.golang.org/api/option"
)

var firebaseAuth *auth.Client

func InitFirebase() error {
	credsPath := os.Getenv("FIREBASE_CREDENTIALS_PATH_AXIS")
	if credsPath == "" {
		log.Println("Warning: FIREBASE_CREDENTIALS_PATH not set")
		return nil
	}

	opt := option.WithCredentialsFile(credsPath)
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		return fmt.Errorf("failed to create Firebase app: %v", err)
	}

	firebaseAuth, err = app.Auth(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get Firebase auth client: %v", err)
	}

	log.Println("Firebase initialized successfully")
	return nil
}

// POST /api/v1/riders/google-login
func GoogleLogin(c *gin.Context) {
	var req struct {
		IDToken string `json:"id_token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID token required"})
		return
	}

	// Verify Firebase token
	token, err := firebaseAuth.VerifyIDToken(context.Background(), req.IDToken)
	if err != nil {
		log.Printf("Firebase token verification failed: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid Google token"})
		return
	}

	// Extract user info from Firebase token
	email, ok := token.Claims["email"].(string)
	if !ok || email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No email found in Google account"})
		return
	}

	googleID := token.UID
	name, _ := token.Claims["name"].(string)
	emailVerified, _ := token.Claims["email_verified"].(bool)
	picture, _ := token.Claims["picture"].(string)

	// Verify email is verified by Google
	if !emailVerified {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Email not verified by Google"})
		return
	}

	// Find or create rider
	var rider models.Rider
	err = config.DB.Where("email = ?", email).First(&rider).Error

	if err != nil {
		// Create new rider with Google login
		rider = models.Rider{
			ID:          uuid.New().String(),
			Email:       email,
			GoogleID:    googleID,
			IsVerified:  true,
			FirstName:   extractFirstName(name),
			LastName:    extractLastName(name),
			AvatarURL:   picture,
			LastLoginAt: nil,
		}

		if err := config.DB.Create(&rider).Error; err != nil {
			log.Printf("Failed to create rider: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not create rider"})
			return
		}
	} else {
		// Update existing rider with Google ID if not set
		if rider.GoogleID == "" {
			rider.GoogleID = googleID
		}
		if rider.FirstName == "" && name != "" {
			rider.FirstName = extractFirstName(name)
		}
		if rider.AvatarURL == "" && picture != "" {
			rider.AvatarURL = picture
		}
		config.DB.Save(&rider)
	}

	// Update last login
	now := time.Now()
	rider.LastLoginAt = &now
	config.DB.Save(&rider)

	// Generate JWT
	tokenString := generateRiderJWT(rider.ID, rider.Email)

	c.JSON(http.StatusOK, gin.H{
		"rider_id":     rider.ID,
		"email":        rider.Email,
		"first_name":   rider.FirstName,
		"avatar_url":   rider.AvatarURL,
		"access_token": tokenString,
		"token_type":   "bearer",
		"is_new":       rider.CreatedAt == rider.UpdatedAt,
	})
}

// Helper functions
func extractFirstName(fullName string) string {
	parts := strings.Split(strings.TrimSpace(fullName), " ")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func extractLastName(fullName string) string {
	parts := strings.Split(strings.TrimSpace(fullName), " ")
	if len(parts) > 1 {
		return strings.Join(parts[1:], " ")
	}
	return ""
}

func generateRiderJWT(riderID, email string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"rider_id": riderID,
		"email":    email,
		"exp":      time.Now().Add(30 * 24 * time.Hour).Unix(),
	})

	tokenString, _ := token.SignedString([]byte(os.Getenv("JWT_SECRET")))
	return tokenString
}

// Alternative: Verify Google token without Firebase SDK
func GoogleLoginDirect(c *gin.Context) {
	var req struct {
		IDToken string `json:"id_token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID token required"})
		return
	}

	// Verify with Google's tokeninfo endpoint
	resp, err := http.Get(fmt.Sprintf("https://oauth2.googleapis.com/tokeninfo?id_token=%s", req.IDToken))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
		return
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read response"})
		return
	}

	var tokenInfo struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
		Sub           string `json:"sub"`
		Aud           string `json:"aud"`
	}
	if err := json.Unmarshal(body, &tokenInfo); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}

	// Verify the token is for your app
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	if tokenInfo.Aud != clientID {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token audience"})
		return
	}

	if !tokenInfo.EmailVerified {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Email not verified"})
		return
	}

	// Find or create rider
	var rider models.Rider
	err = config.DB.Where("email = ? OR google_id = ?", tokenInfo.Email, tokenInfo.Sub).First(&rider).Error

	if err != nil {
		// Create new rider with Google login
		rider = models.Rider{
			ID:          uuid.New().String(),
			Email:       tokenInfo.Email,
			GoogleID:    tokenInfo.Sub,
			IsVerified:  true,
			FirstName:   extractFirstName(tokenInfo.Name),
			LastName:    extractLastName(tokenInfo.Name),
			AvatarURL:   tokenInfo.Picture,
			LastLoginAt: nil,
		}

		if err := config.DB.Create(&rider).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not create rider"})
			return
		}
	} else {
		// Update existing rider with Google ID if not set
		if rider.GoogleID == "" {
			rider.GoogleID = tokenInfo.Sub
		}
		if rider.FirstName == "" && tokenInfo.Name != "" {
			rider.FirstName = extractFirstName(tokenInfo.Name)
		}
		if rider.AvatarURL == "" && tokenInfo.Picture != "" {
			rider.AvatarURL = tokenInfo.Picture
		}
		config.DB.Save(&rider)
	}

	// Update last login
	now := time.Now()
	rider.LastLoginAt = &now
	config.DB.Save(&rider)

	// Generate JWT
	tokenString := generateRiderJWT(rider.ID, rider.Email)

	c.JSON(http.StatusOK, gin.H{
		"rider_id":     rider.ID,
		"email":        rider.Email,
		"first_name":   rider.FirstName,
		"avatar_url":   rider.AvatarURL,
		"access_token": tokenString,
		"token_type":   "bearer",
		"is_new":       rider.CreatedAt == rider.UpdatedAt,
	})
}