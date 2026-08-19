package handlers

import (
	"log"
	"net/http"
	"os"
	"time"
	"strings"
	"math/rand"

	"goapi/config"
	"goapi/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetDriver(c *gin.Context) {
    sessionID := c.Query("session_id")
    if sessionID == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
        return
    }

    currentDriverID := c.Query("driver_id")

    var driver models.DriverResponse
    err := config.DB.Transaction(func(tx *gorm.DB) error {
        // Delete current assignment
        if currentDriverID != "" {
            tx.Where("session_id = ? AND driver_id = ?", sessionID, currentDriverID).
                Delete(&models.DriverAssignment{})
        }

        // Delete expired assignments
        tx.Where("expires_at < ?", time.Now()).
            Delete(&models.DriverAssignment{})

        // Count available drivers first
        var total int
        tx.Raw(`
            SELECT COUNT(*) FROM drivers d
            WHERE d.is_online = 1
              AND d.is_active = 1
              AND d.account_status = 'active'
              AND d.device_token IS NOT NULL
              AND d.phone != '550880119'
              AND d.id NOT IN (
                SELECT driver_id FROM driver_assignments WHERE expires_at > NOW()
              )
              AND d.id NOT IN (
                SELECT driver_id FROM bookings WHERE status IN ('confirmed', 'in_progress')
              )
              AND d.id != ?
        `, currentDriverID).Scan(&total)

        if total == 0 {
            return gorm.ErrRecordNotFound
        }

        // Pick a random offset
        offset := rand.Intn(total)

        result := tx.Raw(`
            SELECT 
                d.id,
                d.display_id as driver_number,
                CASE 
                    WHEN d.first_name IS NOT NULL AND d.last_name IS NOT NULL THEN CONCAT(d.first_name, ' ', d.last_name)
                    WHEN d.first_name IS NOT NULL THEN d.first_name
                    WHEN d.last_name IS NOT NULL THEN d.last_name
                    ELSE 'Driver'
                END as username,
                d.phone,
                COALESCE(d.avatar_url, '') as avatar,
                COALESCE(dv.car_make, '') as car_make,
                COALESCE(dv.car_model, '') as car_model,
                COALESCE(dv.year, '') as year,
                COALESCE(dv.bay, '') as bay,
                COALESCE(dv.plate_number, '') as plate_number
            FROM drivers d
            LEFT JOIN driver_vehicles dv ON dv.driver_id = d.id AND dv.is_active = 1
            WHERE d.is_online = 1
              AND d.is_active = 1
              AND d.account_status = 'active'
              AND d.device_token IS NOT NULL
              AND d.id NOT IN (
                SELECT driver_id FROM driver_assignments WHERE expires_at > NOW()
              )
              AND d.id NOT IN (
                SELECT driver_id FROM bookings WHERE status IN ('confirmed', 'in_progress')
              )
              AND d.id != ?
            LIMIT 1 OFFSET ?
        `, currentDriverID, offset).Scan(&driver)

        if result.Error != nil {
            return result.Error
        }
        if driver.ID == "" {
            return gorm.ErrRecordNotFound
        }

        return tx.Exec(`
            INSERT INTO driver_assignments (driver_id, session_id, assigned_at, expires_at)
            VALUES (?, ?, ?, ?)
            ON DUPLICATE KEY UPDATE
                session_id = VALUES(session_id),
                assigned_at = VALUES(assigned_at),
                expires_at = VALUES(expires_at)
        `, driver.ID, sessionID, time.Now(), time.Now().Add(5*time.Minute)).Error
    })

    if err != nil {
        if err == gorm.ErrRecordNotFound {
            c.JSON(http.StatusServiceUnavailable, gin.H{"error": "No drivers available right now"})
            return
        }
        log.Println("Assignment error:", err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not assign driver"})
        return
    }

    baseURL := os.Getenv("APP_URL")
    if driver.Avatar != "" && !strings.HasPrefix(driver.Avatar, "http") {
        driver.Avatar = baseURL + "/images/avatar/" + driver.Avatar
    }

    c.JSON(http.StatusOK, gin.H{"driver": driver})
}

func GetActiveAssignments(c *gin.Context) {
	type Row struct {
		DriverID   string    `json:"driver_id"`
		DriverName string    `json:"driver_name"`
		SessionID  string    `json:"session_id"`
		AssignedAt time.Time `json:"assigned_at"`
		ExpiresAt  time.Time `json:"expires_at"`
	}
	var rows []Row
	config.DB.Raw(`
		SELECT da.driver_id, COALESCE(d.first_name, 'Driver') as driver_name, 
		       da.session_id, da.assigned_at, da.expires_at
		FROM driver_assignments da
		JOIN drivers d ON d.id = da.driver_id
		ORDER BY da.assigned_at ASC
	`).Scan(&rows)
	c.JSON(http.StatusOK, gin.H{"active_assignments": rows})
}
