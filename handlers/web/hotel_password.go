package web
import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"goapi/config"
	"goapi/models"
)

const (
	resetTokenTTL  = 1 * time.Hour
	inviteTokenTTL = 7 * 24 * time.Hour
	minPasswordLen = 8
)

// SendHotelPasswordLink delivers the set/reset link. Override it in
// InitEmailService with your EmailService, e.g.
//
//	web.SendHotelPasswordLink = func(to, hotelName, link, purpose string) error {
//	    return emailService.SendHotelPasswordLinkEmail(to, hotelName, link, purpose)
//	}
//
// purpose is "invite" (first-time password after approval) or "reset".
var SendHotelPasswordLink = func(to, hotelName, link, purpose string) error {
	slog.Warn("SendHotelPasswordLink not configured; link not emailed", "to", to, "purpose", purpose, "link", link)
	return nil
}

// ── token helpers ─────────────────────────────────────────────────────────────

func newRawToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashTokenPass(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// issueHotelPasswordToken invalidates older unused tokens for the member and
// stores a new one. Returns the raw token to embed in the link.
func issueHotelPasswordToken(memberID, purpose string, ttl time.Duration) (string, error) {
	raw := newRawToken()
	now := time.Now()

	config.DB.Model(&models.HotelPasswordToken{}).
		Where("member_id = ? AND used_at IS NULL", memberID).
		Update("used_at", now)

	tok := models.HotelPasswordToken{
		ID:        uuid.New().String(),
		MemberID:  memberID,
		TokenHash: hashTokenPass(raw),
		Purpose:   purpose,
		ExpiresAt: now.Add(ttl),
	}
	if err := config.DB.Create(&tok).Error; err != nil {
		return "", err
	}
	return raw, nil
}

func passwordLink(raw string) string {
	return strings.TrimRight(os.Getenv("APP_URL_MAIN"), "/") + "/reset-password?token=" + raw
}

// lookupToken returns the token row, member and hotel for a valid, unused, unexpired raw token.
func lookupToken(raw string) (*models.HotelPasswordToken, *models.HotelMember, *models.Hotel, string) {
	if raw == "" || len(raw) > 128 {
		return nil, nil, nil, "This link is invalid."
	}
	var tok models.HotelPasswordToken
	if err := config.DB.Where("token_hash = ?", hashTokenPass(raw)).First(&tok).Error; err != nil {
		return nil, nil, nil, "This link is invalid."
	}
	if tok.UsedAt != nil {
		return nil, nil, nil, "This link has already been used. Request a new one."
	}
	if time.Now().After(tok.ExpiresAt) {
		return nil, nil, nil, "This link has expired. Request a new one."
	}
	var member models.HotelMember
	if err := config.DB.Where("id = ?", tok.MemberID).First(&member).Error; err != nil {
		return nil, nil, nil, "This account no longer exists."
	}
	var hotel models.Hotel
	config.DB.Where("id = ?", member.HotelID).First(&hotel)
	if hotel.Status != "active" {
		return nil, nil, nil, "This property is not active. Contact Axis support."
	}
	return &tok, &member, &hotel, ""
}

// ── public handlers ───────────────────────────────────────────────────────────

// ForgotHotelPassword always returns 200 so the endpoint cannot be used to
// enumerate partner emails.
func ForgotHotelPassword(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid email is required"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))

	go func() {
		var member models.HotelMember
		if err := config.DB.Where("LOWER(email) = ? AND is_active = 1", email).First(&member).Error; err != nil {
			return
		}
		var hotel models.Hotel
		config.DB.Where("id = ?", member.HotelID).First(&hotel)
		if hotel.Status != "active" {
			return
		}
		// A member who never set a password gets an invite-style link instead.
		purpose, ttl := "reset", resetTokenTTL
		if member.PasswordHash == "" {
			purpose, ttl = "invite", inviteTokenTTL
		}
		raw, err := issueHotelPasswordToken(member.ID, purpose, ttl)
		if err != nil {
			slog.Error("hotel forgot: issue token", "error", err)
			return
		}
		if err := SendHotelPasswordLink(member.Email, hotel.Name, passwordLink(raw), purpose); err != nil {
			slog.Error("hotel forgot: send email", "error", err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "If that email belongs to an approved partner account, a reset link is on its way.",
	})
}

// ValidateHotelPasswordToken lets the reset page show who the link is for.
func ValidateHotelPasswordToken(c *gin.Context) {
	tok, member, hotel, problem := lookupToken(c.Query("token"))
	if problem != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": problem})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"email":     member.Email,
		"hotelName": hotel.Name,
		"purpose":   tok.Purpose,
	})
}

// ResetHotelPassword sets the password, burns the token and logs the member in.
func ResetHotelPassword(c *gin.Context) {
	var req struct {
		Token    string `json:"token" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token and password are required"})
		return
	}
	if len(req.Password) < minPasswordLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("password must be at least %d characters", minPasswordLen)})
		return
	}
	if len(req.Password) > 72 { // bcrypt input limit
		c.JSON(http.StatusBadRequest, gin.H{"error": "password is too long"})
		return
	}

	tok, member, _, problem := lookupToken(req.Token)
	if problem != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": problem})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save password"})
		return
	}

	now := time.Now()
	tx := config.DB.Begin()
	if err := tx.Model(&models.HotelMember{}).Where("id = ?", member.ID).
		Updates(map[string]interface{}{"password_hash": string(hash), "is_active": true}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save password"})
		return
	}
	if err := tx.Model(&models.HotelPasswordToken{}).Where("id = ?", tok.ID).
		Update("used_at", now).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save password"})
		return
	}
	tx.Commit()

	token := generateHotelJWT(member.ID, member.HotelID, member.Role)
	c.JSON(http.StatusOK, gin.H{
		"token":        token,
		"isFirstLogin": member.FirstLoginAt == nil,
		"member": gin.H{
			"id":      member.ID,
			"email":   member.Email,
			"role":    member.Role,
			"hotelId": member.HotelID,
		},
	})
}
