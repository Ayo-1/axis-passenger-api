package web

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"goapi/config"
	"goapi/models"
)

// ── Storage ──────────────────────────────────────────────────
//
// KYC documents are written to KYC_STORAGE_DIR (default /var/lib/axis/kyc),
// which must NOT be served by nginx. Keys look like kyc/2026/09/<uuid>.<ext>
// and are the only thing ever returned to clients.

const (
	maxKYCUploadBytes   = 8 << 20
	kycUploadsPerMinute = 20
)

var (
	kycKinds = map[string]bool{
		"id_front": true, "id_back": true,
		"licence_front": true, "licence_back": true,
		"selfie": true,
	}
	kycMimeExt = map[string]string{
		"image/jpeg":      "jpg",
		"image/png":       "png",
		"image/webp":      "webp",
		"application/pdf": "pdf",
	}
	kycKeyPattern = regexp.MustCompile(`^kyc/\d{4}/\d{2}/[a-f0-9-]{36}\.(jpg|jpeg|png|webp|pdf)$`)
)

func kycStorageDir() string {
	if dir := os.Getenv("KYC_STORAGE_DIR"); dir != "" {
		return dir
	}
	return "/var/lib/axis/kyc"
}

// kycObjectExists checks a client-supplied key is well formed and stored.
func kycObjectExists(key string) bool {
	if !kycKeyPattern.MatchString(key) {
		return false
	}
	info, err := os.Stat(filepath.Join(kycStorageDir(), filepath.FromSlash(key)))
	return err == nil && info.Mode().IsRegular()
}

// allowKYCUpload is a fixed one-minute window per client IP in Redis.
// If Redis is unavailable it fails open so bookings aren't blocked.
func allowKYCUpload(c *gin.Context) bool {
	if config.RedisClient == nil {
		return true
	}
	ctx := c.Request.Context()
	key := "rl:kyc_upload:" + c.ClientIP()
	n, err := config.RedisClient.Incr(ctx, key).Result()
	if err != nil {
		slog.Warn("kyc upload rate limit unavailable", "error", err)
		return true
	}
	if n == 1 {
		config.RedisClient.Expire(ctx, key, time.Minute)
	}
	return n <= kycUploadsPerMinute
}

// POST /v1/app/rentals/kyc/upload  (multipart: file, kind)
func UploadRentalKYC(c *gin.Context) {
	if !allowKYCUpload(c) {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many uploads, try again in a minute"})
		return
	}

	// Small allowance on top of the file for the multipart envelope and `kind`.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxKYCUploadBytes+64<<10)

	// Parse once, up front, so an oversized body is reported as such.
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file must be 8 MB or smaller"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected a multipart form with file and kind"})
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll() // temp files for large parts
	}

	kind := c.Request.FormValue("kind")
	if !kycKinds[kind] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be one of id_front, id_back, licence_front, licence_back, selfie"})
		return
	}

	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	if fh.Size <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is empty"})
		return
	}
	if fh.Size > maxKYCUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file must be 8 MB or smaller"})
		return
	}

	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not read file"})
		return
	}
	defer src.Close()

	// Sniff the real type; the client's Content-Type header is ignored.
	head := make([]byte, 512)
	n, err := io.ReadFull(src, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not read file"})
		return
	}
	mime := http.DetectContentType(head[:n])
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	ext, ok := kycMimeExt[mime]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file must be a JPEG, PNG, WebP or PDF"})
		return
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read file"})
		return
	}

	key := fmt.Sprintf("kyc/%s/%s.%s", time.Now().UTC().Format("2006/01"), uuid.New().String(), ext)
	path := filepath.Join(kycStorageDir(), filepath.FromSlash(key))

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		slog.Error("kyc storage mkdir", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not store file"})
		return
	}
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		slog.Error("kyc storage create", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not store file"})
		return
	}
	written, copyErr := io.Copy(dst, io.LimitReader(src, maxKYCUploadBytes+1))
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil || written > maxKYCUploadBytes {
		os.Remove(path)
		if written > maxKYCUploadBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file must be 8 MB or smaller"})
			return
		}
		slog.Error("kyc storage write", "copy_error", copyErr, "close_error", closeErr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not store file"})
		return
	}

	slog.Info("kyc document uploaded", "kind", kind, "key", key, "bytes", written, "mime", mime)
	c.JSON(http.StatusOK, gin.H{"key": key})
}

// ── Request shape ────────────────────────────────────────────
//
// Plain strings throughout: the frontend omits arrivalDate and idBackKey for a
// Ghana Card, and every document key is optional. Presence rules are enforced
// in validate(), not with binding tags.

type KYCDocuments struct {
	IDFrontKey      string `json:"idFrontKey"`
	IDBackKey       string `json:"idBackKey"`
	LicenceFrontKey string `json:"licenceFrontKey"`
	LicenceBackKey  string `json:"licenceBackKey"`
	SelfieKey       string `json:"selfieKey"`
}

type KYCInput struct {
	IDType         string       `json:"idType"`
	IDNumber       string       `json:"idNumber"`
	IDExpiry       string       `json:"idExpiry"`
	Nationality    string       `json:"nationality"`
	DateOfBirth    string       `json:"dateOfBirth"`
	HomeAddress    string       `json:"homeAddress"`
	ArrivalDate    string       `json:"arrivalDate"`
	LicenceNumber  string       `json:"licenceNumber"`
	LicenceCountry string       `json:"licenceCountry"`
	LicenceExpiry  string       `json:"licenceExpiry"`
	Documents      KYCDocuments `json:"documents"`
}

func parseKYCDate(field, v string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return time.Time{}, fmt.Errorf("kyc.%s must be a date in YYYY-MM-DD format", field)
	}
	return t, nil
}

// toModel validates the KYC details and returns the row to insert (without
// BookingID). pickup is the rental start, used for the minimum-age check.
func (k *KYCInput) toModel(pickup time.Time) (*models.RentalKYC, error) {
	if k == nil {
		return nil, errors.New("kyc is required")
	}
	trim := func(s *string) { *s = strings.TrimSpace(*s) }
	for _, f := range []*string{&k.IDType, &k.IDNumber, &k.Nationality, &k.HomeAddress, &k.LicenceNumber, &k.LicenceCountry} {
		trim(f)
	}

	if k.IDType != "ghana_card" && k.IDType != "passport" {
		return nil, errors.New("kyc.idType must be ghana_card or passport")
	}
	if len(k.IDNumber) < 5 {
		return nil, errors.New("kyc.idNumber must be at least 5 characters")
	}
	if len(k.LicenceNumber) < 4 {
		return nil, errors.New("kyc.licenceNumber must be at least 4 characters")
	}
	if len(k.HomeAddress) < 6 {
		return nil, errors.New("kyc.homeAddress must be at least 6 characters")
	}
	if k.LicenceCountry == "" {
		return nil, errors.New("kyc.licenceCountry is required")
	}

	today := time.Now().In(accraLoc()).Format("2006-01-02")

	idExpiry, err := parseKYCDate("idExpiry", k.IDExpiry)
	if err != nil {
		return nil, err
	}
	if idExpiry.Format("2006-01-02") < today {
		return nil, errors.New("kyc.idExpiry: the ID has expired")
	}
	licenceExpiry, err := parseKYCDate("licenceExpiry", k.LicenceExpiry)
	if err != nil {
		return nil, err
	}
	if licenceExpiry.Format("2006-01-02") < today {
		return nil, errors.New("kyc.licenceExpiry: the driving licence has expired")
	}

	dob, err := parseKYCDate("dateOfBirth", k.DateOfBirth)
	if err != nil {
		return nil, err
	}
	pickupDay := pickup.In(accraLoc()).Format("2006-01-02")
	if dob.AddDate(21, 0, 0).Format("2006-01-02") > pickupDay {
		return nil, errors.New("kyc.dateOfBirth: the driver must be at least 21 years old on the pickup date")
	}

	var arrival *time.Time
	switch k.IDType {
	case "ghana_card":
		k.Nationality = "Ghana" // arrivalDate is not collected for residents
	case "passport":
		if k.Nationality == "" {
			return nil, errors.New("kyc.nationality is required for a passport")
		}
		if strings.TrimSpace(k.ArrivalDate) == "" {
			return nil, errors.New("kyc.arrivalDate is required for a passport")
		}
		a, err := parseKYCDate("arrivalDate", k.ArrivalDate)
		if err != nil {
			return nil, err
		}
		if a.Format("2006-01-02") > today {
			return nil, errors.New("kyc.arrivalDate cannot be in the future")
		}
		arrival = &a
	}

	docKey := func(v string) (*string, error) {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, nil
		}
		if !kycObjectExists(v) {
			return nil, errors.New("invalid document reference")
		}
		return &v, nil
	}
	row := &models.RentalKYC{
		ID:             uuid.New().String(),
		IDType:         k.IDType,
		IDNumber:       k.IDNumber,
		IDExpiry:       &idExpiry,
		Nationality:    k.Nationality,
		DateOfBirth:    &dob,
		HomeAddress:    k.HomeAddress,
		ArrivalDate:    arrival,
		LicenceNumber:  k.LicenceNumber,
		LicenceCountry: k.LicenceCountry,
		LicenceExpiry:  &licenceExpiry,
	}
	for _, d := range []struct {
		in  string
		out **string
	}{
		{k.Documents.IDFrontKey, &row.IDFrontKey},
		{k.Documents.IDBackKey, &row.IDBackKey},
		{k.Documents.LicenceFrontKey, &row.LicenceFrontKey},
		{k.Documents.LicenceBackKey, &row.LicenceBackKey},
		{k.Documents.SelfieKey, &row.SelfieKey},
	} {
		if *d.out, err = docKey(d.in); err != nil {
			return nil, err
		}
	}
	return row, nil
}

func accraLoc() *time.Location {
	if loc, err := time.LoadLocation("Africa/Accra"); err == nil {
		return loc
	}
	return time.UTC
}

// createRentalWithKYC inserts the booking and its KYC row in one transaction,
// so a failed KYC insert rolls the booking back.
func createRentalWithKYC(booking *models.BookingSchedule, kyc *models.RentalKYC) error {
	return config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(booking).Error; err != nil {
			return err
		}
		kyc.BookingID = booking.ID
		return tx.Create(kyc).Error
	})
}

// deleteRentalWithKYC removes a rental whose payment link couldn't be created.
// Uploaded files are kept: the frontend may retry with the same keys.
func deleteRentalWithKYC(booking *models.BookingSchedule) {
	config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("booking_id = ?", booking.ID).Delete(&models.RentalKYC{}).Error; err != nil {
			return err
		}
		return tx.Delete(booking).Error
	})
}

// PurgeExpiredRentalKYC deletes KYC rows, and their files, for rentals whose
// payment has definitely failed: the booking is expired, unpaid, and no
// payment intent for it is still pending or paid (intents live 24h, so a
// late payment can't confirm a booking whose KYC is gone). A file is only
// removed when no remaining KYC row references it. Called by the booking
// cleanup worker.
func PurgeExpiredRentalKYC() {
	var rows []models.RentalKYC
	if err := config.DB.Raw(`
		SELECT k.* FROM rental_kyc k
		JOIN bookings b ON b.id = k.booking_id
		WHERE b.service_type = 'rental'
		  AND b.status = 'expired'
		  AND b.payment_status <> 'paid'
		  AND NOT EXISTS (
		    SELECT 1 FROM payment_intents pi
		    WHERE pi.reference = b.session_id AND pi.status IN ('pending', 'paid')
		  )
		LIMIT 200
	`).Scan(&rows).Error; err != nil {
		slog.Error("purge expired rental kyc: load", "error", err)
		return
	}

	for _, k := range rows {
		if err := config.DB.Where("id = ?", k.ID).Delete(&models.RentalKYC{}).Error; err != nil {
			slog.Error("purge expired rental kyc: delete row", "kyc_id", k.ID, "error", err)
			continue
		}

		for _, key := range []*string{k.IDFrontKey, k.IDBackKey, k.LicenceFrontKey, k.LicenceBackKey, k.SelfieKey} {
			if key == nil || !kycKeyPattern.MatchString(*key) {
				continue
			}
			var stillUsed int64
			config.DB.Model(&models.RentalKYC{}).
				Where("id_front_key = ? OR id_back_key = ? OR licence_front_key = ? OR licence_back_key = ? OR selfie_key = ?",
					*key, *key, *key, *key, *key).
				Count(&stillUsed)
			if stillUsed > 0 {
				continue
			}
			path := filepath.Join(kycStorageDir(), filepath.FromSlash(*key))
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				slog.Error("purge expired rental kyc: delete file", "key", *key, "error", err)
			}
		}
		slog.Info("purged kyc for expired rental", "booking_id", k.BookingID)
	}
}

// ── Responses ────────────────────────────────────────────────

// kycSummary is the non-file part of a KYC row, for dashboards and receipts.
func kycSummary(k models.RentalKYC) gin.H {
	date := func(t *time.Time) interface{} {
		if t == nil {
			return nil
		}
		return t.Format("2006-01-02")
	}
	return gin.H{
		"idType":         k.IDType,
		"idNumber":       k.IDNumber,
		"idExpiry":       date(k.IDExpiry),
		"nationality":    k.Nationality,
		"dateOfBirth":    date(k.DateOfBirth),
		"homeAddress":    k.HomeAddress,
		"arrivalDate":    date(k.ArrivalDate),
		"licenceNumber":  k.LicenceNumber,
		"licenceCountry": k.LicenceCountry,
		"licenceExpiry":  date(k.LicenceExpiry),
	}
}

// rentalKYCSummary returns the summary for a rental booking, or nil.
func rentalKYCSummary(b *models.BookingSchedule) gin.H {
	if b.ServiceType != "rental" {
		return nil
	}
	var k models.RentalKYC
	if err := config.DB.Where("booking_id = ?", b.ID).Order("created_at DESC").First(&k).Error; err != nil {
		return nil
	}
	return kycSummary(k)
}

// rentalKYCSummaries loads summaries for many bookings in one query.
func rentalKYCSummaries(bookingIDs []uint) map[uint]gin.H {
	out := map[uint]gin.H{}
	if len(bookingIDs) == 0 {
		return out
	}
	var rows []models.RentalKYC
	config.DB.Where("booking_id IN ?", bookingIDs).Order("created_at ASC").Find(&rows)
	for _, k := range rows {
		out[k.BookingID] = kycSummary(k) // latest wins
	}
	return out
}
