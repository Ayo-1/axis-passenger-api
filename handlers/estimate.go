package handlers

import (
	"math"
	"net/http"
	"time"

	"goapi/config"
	"goapi/models"
	"goapi/services"

	"github.com/gin-gonic/gin"
)

const (
	baseFare        = 10.0
	pricePerKm      = 6.3
	minimumBaseFare = 18.0
)

type EstimateRequest struct {
	SessionID      string  `json:"session_id" binding:"required"`
	DriverID       string  `json:"driver_id" binding:"required"`
	DropoffLat     float64 `json:"dropoff_lat" binding:"required"`
	DropoffLng     float64 `json:"dropoff_lng" binding:"required"`
	DropoffAddress string  `json:"dropoff_address"`
	PickupLat      float64 `json:"pickup_lat" binding:"required"`
	PickupLng      float64 `json:"pickup_lng" binding:"required"`
	PickupAddress  string  `json:"pickup_address"`
}

func GetEstimate(c *gin.Context) {
	var req EstimateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id, driver_id, pickup_lat, pickup_lng, dropoff_lat and dropoff_lng are required"})
		return
	}

	if req.DropoffLat < -90 || req.DropoffLat > 90 || req.DropoffLng < -180 || req.DropoffLng > 180 ||
		req.PickupLat < -90 || req.PickupLat > 90 || req.PickupLng < -180 || req.PickupLng > 180 {
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

	route, err := services.GetRouteInfo(req.PickupLat, req.PickupLng, req.DropoffLat, req.DropoffLng)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Could not calculate route"})
		return
	}

	calculated := baseFare + (route.DistanceKm * pricePerKm)
	fare := math.Max(minimumBaseFare, calculated)

	pickupLabel := req.PickupAddress
	if pickupLabel == "" {
		pickupLabel = "Airport"
	}

	c.JSON(http.StatusOK, gin.H{"estimate": gin.H{
		"session_id":    req.SessionID,
		"driver_id":     req.DriverID,
		"pickup":        pickupLabel,
		"dropoff":       req.DropoffAddress,
		"distance_km":   math.Round(route.DistanceKm*10) / 10,
		"distance_text": route.DistanceText,
		"duration_text": route.DurationText,
		"fare_low":      math.Round(fare*0.92*10) / 10,
		"fare_high":     math.Round(fare*1.15*10) / 10,
		"currency":      "GHC",
	}})
}
