package handlers

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"time"

	"goapi/config"
	"goapi/models"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type appleJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type appleJWKS struct {
	Keys []appleJWK `json:"keys"`
}

func fetchAppleJWKS() (*appleJWKS, error) {
	resp, err := http.Get("https://appleid.apple.com/auth/keys")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var jwks appleJWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, err
	}
	return &jwks, nil
}

func jwkToRSAPublicKey(k appleJWK) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, err
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

func verifyAppleIDToken(idToken string) (jwt.MapClaims, error) {
	jwks, err := fetchAppleJWKS()
	if err != nil {
		return nil, fmt.Errorf("fetch apple keys: %w", err)
	}

	token, err := jwt.Parse(idToken, func(t *jwt.Token) (interface{}, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("missing kid in token header")
		}
		for _, k := range jwks.Keys {
			if k.Kid == kid {
				return jwkToRSAPublicKey(k)
			}
		}
		return nil, fmt.Errorf("key %s not found", kid)
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid apple token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims")
	}

	if iss, _ := claims["iss"].(string); iss != "https://appleid.apple.com" {
		return nil, fmt.Errorf("invalid issuer: %s", iss)
	}
	aud, _ := claims["aud"].(string)
	if aud != os.Getenv("APPLE_CLIENT_ID") {
		return nil, fmt.Errorf("invalid audience: %s", aud)
	}

	return claims, nil
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

	claims, err := verifyAppleIDToken(req.IDToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid Apple identity token"})
		return
	}

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