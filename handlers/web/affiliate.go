package web

import (
	"crypto/rand"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goapi/config"
	"goapi/models"
)

// ── Affiliate (referral) links ───────────────────────────────
//
// Each hotel gets a referral code. Customers who book from the public site
// through the hotel's link (…/?ref=CODE, passed to the API as referralCode)
// pay for themselves; the booking is stored with the hotel's hotel_id and
// channel "affiliate" , and earns the hotel its usual commission once paid.

const referralAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I

func newReferralCode() string {
	b := make([]byte, 8)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(referralAlphabet))))
		b[i] = referralAlphabet[n.Int64()]
	}
	return string(b)
}

// ensureReferralCode gives the hotel a code the first time it's needed.
func ensureReferralCode(hotel *models.Hotel) (string, error) {
	if hotel.ReferralCode != nil && *hotel.ReferralCode != "" {
		return *hotel.ReferralCode, nil
	}
	var lastErr error
	for i := 0; i < 5; i++ { // retry on the (unlikely) unique-key clash
		code := newReferralCode()
		res := config.DB.Model(&models.Hotel{}).
			Where("id = ? AND (referral_code IS NULL OR referral_code = '')", hotel.ID).
			Update("referral_code", code)
		if res.Error != nil {
			lastErr = res.Error
			continue
		}
		var fresh models.Hotel
		if err := config.DB.Select("referral_code").Where("id = ?", hotel.ID).First(&fresh).Error; err == nil && fresh.ReferralCode != nil {
			hotel.ReferralCode = fresh.ReferralCode
			return *fresh.ReferralCode, nil
		}
	}
	return "", lastErr
}

// resolveReferral returns the hotel ID for an active hotel's referral code,
// or nil. An unknown code never blocks a booking; it's just not attributed.
func resolveReferral(code string) *string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil
	}
	var hotel models.Hotel
	if err := config.DB.Select("id").Where("referral_code = ? AND status = 'active'", code).First(&hotel).Error; err != nil {
		log.Printf("referral code %q not matched: %v", code, err)
		return nil
	}
	id := hotel.ID
	return &id
}

func referralLinks(code string) gin.H {
	base := strings.TrimRight(os.Getenv("APP_URL_MAIN"), "/")
	return gin.H{
		"booking": base + "/?ref=" + code,
		"rentals": base + "/rentals?ref=" + code,
	}
}

// GET /v1/app/ref/:code — public; lets the site show "Booking via <hotel>".
func GetReferral(c *gin.Context) {
	code := strings.ToUpper(strings.TrimSpace(c.Param("code")))
	var hotel models.Hotel
	if code == "" || config.DB.Select("name").Where("referral_code = ? AND status = 'active'", code).First(&hotel).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "referral code not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": code, "hotelName": hotel.Name})
}

// GET /v1/app/hotels/affiliate — the hotel's link and how it's performing.
func HotelAffiliate(c *gin.Context) {
	hotelID := c.GetString("hotel_id")

	var hotel models.Hotel
	if err := config.DB.Where("id = ? AND status = 'active'", hotelID).First(&hotel).Error; err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "hotel not active"})
		return
	}

	code, err := ensureReferralCode(&hotel)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create referral code"})
		return
	}

	var stats struct {
		Bookings     int64   `gorm:"column:bookings"`
		PaidBookings int64   `gorm:"column:paid_bookings"`
		Pending      float64 `gorm:"column:pending"`
		Available    float64 `gorm:"column:available"`
		PaidOut      float64 `gorm:"column:paid_out"`
	}
	config.DB.Raw(`
		SELECT
			COUNT(DISTINCT b.id) AS bookings,
			COUNT(DISTINCT CASE WHEN b.payment_status = 'paid' THEN b.id END) AS paid_bookings,
			COALESCE(SUM(CASE WHEN cm.status = 'pending' THEN cm.amount END), 0) AS pending,
			COALESCE(SUM(CASE WHEN cm.status = 'available' THEN cm.amount END), 0) AS available,
			COALESCE(SUM(CASE WHEN cm.status IN ('processing', 'paid') THEN cm.amount END), 0) AS paid_out
		FROM bookings b
		LEFT JOIN commissions cm ON cm.booking_id = b.id
		WHERE b.hotel_id = ? AND b.channel = 'affiliate'
	`, hotelID).Scan(&stats)

	c.JSON(http.StatusOK, gin.H{
		"referralCode":   code,
		"links":          referralLinks(code),
		"commissionRate": hotel.CommissionRate,
		"stats": gin.H{
			"bookings":            stats.Bookings,
			"paidBookings":        stats.PaidBookings,
			"commissionPending":   stats.Pending,
			"commissionAvailable": stats.Available,
			"commissionPaidOut":   stats.PaidOut,
		},
	})
}

// ── Terms & conditions ───────────────────────────────────────
//
// HOTEL_TERMS_VERSION identifies the current terms (bump it when the terms
// change and every hotel is asked to accept again). HOTEL_TERMS_URL is the
// page the dashboard links to.

func currentTermsVersion() string {
	if v := strings.TrimSpace(os.Getenv("HOTEL_TERMS_VERSION")); v != "" {
		return v
	}
	return "1"
}

func termsURL() string {
	if u := os.Getenv("HOTEL_TERMS_URL"); u != "" {
		return u
	}
	return strings.TrimRight(os.Getenv("APP_URL_MAIN"), "/") + "/hotel-terms"
}

// termsStatus is what the dashboard needs to decide whether to prompt.
func termsStatus(h models.Hotel) gin.H {
	current := currentTermsVersion()
	accepted := h.TermsAcceptedAt != nil && h.TermsVersion != nil && *h.TermsVersion == current
	return gin.H{
		"accepted":        accepted,
		"acceptedAt":      h.TermsAcceptedAt,
		"acceptedVersion": h.TermsVersion,
		"currentVersion":  current,
		"url":             termsURL(),
	}
}

func recordTermsAcceptance(hotelID, acceptedBy, ip string) error {
	return config.DB.Model(&models.Hotel{}).Where("id = ?", hotelID).Updates(map[string]interface{}{
		"terms_version":     currentTermsVersion(),
		"terms_accepted_at": time.Now(),
		"terms_accepted_by": acceptedBy,
		"terms_accepted_ip": ip,
	}).Error
}

// POST /v1/app/hotels/terms/accept  {version}
// version must be the current one, so a dashboard showing old terms can't
// record acceptance of terms the hotel never saw.
func AcceptHotelTerms(c *gin.Context) {
	hotelID := c.GetString("hotel_id")
	memberID := c.GetString("member_id")

	var req struct {
		Version string `json:"version" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version is required"})
		return
	}
	if strings.TrimSpace(req.Version) != currentTermsVersion() {
		c.JSON(http.StatusConflict, gin.H{
			"error":          "the terms have been updated; please review the current version",
			"currentVersion": currentTermsVersion(),
			"url":            termsURL(),
		})
		return
	}

	if err := recordTermsAcceptance(hotelID, memberID, c.ClientIP()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record acceptance"})
		return
	}

	var hotel models.Hotel
	config.DB.Where("id = ?", hotelID).First(&hotel)
	c.JSON(http.StatusOK, gin.H{"status": "success", "terms": termsStatus(hotel)})
}
