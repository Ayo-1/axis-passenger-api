package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"time"
	"goapi/config"
    "goapi/models"

	"github.com/gin-gonic/gin"
)

const (
	baseFare        = 10.0
	pricePerKm      = 6.3
	minimumBaseFare = 18.0
	// Kotoka International Airport — from Google Maps
	airportLat = 5.6052
	airportLng = -0.1718
)

type EstimateRequest struct {
	SessionID      string  `json:"session_id" binding:"required"`
	DriverID       string    `json:"driver_id" binding:"required"`
	DropoffLat     float64 `json:"dropoff_lat" binding:"required"`
	DropoffLng     float64 `json:"dropoff_lng" binding:"required"`
	DropoffAddress string  `json:"dropoff_address"` // display name from Places API
}

type mapsResponse struct {
	Rows []struct {
		Elements []struct {
			Status   string `json:"status"`
			Distance struct {
				Value int    `json:"value"`
				Text  string `json:"text"`
			} `json:"distance"`
			Duration struct {
				Value int    `json:"value"`
				Text  string `json:"text"`
			} `json:"duration"`
		} `json:"elements"`
	} `json:"rows"`
	Status string `json:"status"`
}

func GetEstimate(c *gin.Context) {
	var req EstimateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id, driver_id, dropoff_lat and dropoff_lng are required"})
		return
	}

	// Basic coordinate sanity check
	if req.DropoffLat < -90 || req.DropoffLat > 90 || req.DropoffLng < -180 || req.DropoffLng > 180 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid coordinates"})
		return
	}

	var assignment models.DriverAssignment
	if err := config.DB.Where(
		"session_id = ? AND driver_id = ? AND expires_at > ?",
		req.SessionID, req.DriverID, time.Now(),
	).First(&assignment).Error; err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "No active driver assignment, get a driver first"})
		return
	}
	apiKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	mapsURL := fmt.Sprintf(
		"https://maps.googleapis.com/maps/api/distancematrix/json?origins=%f,%f&destinations=%f,%f&key=%s&units=metric",
		airportLat, airportLng,
		req.DropoffLat, req.DropoffLng,
		apiKey,
	)

	resp, err := http.Get(mapsURL)
	if err != nil {
		log.Println("Maps error:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not calculate distance"})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var mapsResp mapsResponse
	json.Unmarshal(body, &mapsResp)

	if mapsResp.Status != "OK" ||
		len(mapsResp.Rows) == 0 ||
		mapsResp.Rows[0].Elements[0].Status != "OK" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Could not calculate route"})
		return
	}

	el := mapsResp.Rows[0].Elements[0]
	distanceKm := float64(el.Distance.Value) / 1000.0
	calculated := baseFare + (distanceKm * pricePerKm)
	fare := math.Max(minimumBaseFare, calculated)

	c.JSON(http.StatusOK, gin.H{"estimate": gin.H{
		"session_id":    req.SessionID,
		"driver_id":     req.DriverID,
		"pickup":        "Kotoka International Airport, Accra",
		"dropoff":       req.DropoffAddress,
		"distance_km":   math.Round(distanceKm*10) / 10,
		"distance_text": el.Distance.Text,
		"duration_text": el.Duration.Text,
		"fare_low":      math.Round(fare*0.92*10) / 10,
		"fare_high":     math.Round(fare*1.15*10) / 10,
		"currency":      "GHC",
	}})
}