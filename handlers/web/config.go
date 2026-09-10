package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"goapi/config"
	"goapi/models"
)

const configCacheKey = "app_config_v2.5"
const configCacheTTL = 30 * time.Minute

func GetAppConfig(c *gin.Context) {
	ctx := context.Background()

	// Try Redis first
	if config.RedisClient != nil {
		cached, err := config.RedisClient.Get(ctx, configCacheKey).Result()
		if err == nil {
			var response models.AppConfigResponse
			if json.Unmarshal([]byte(cached), &response) == nil {
				c.JSON(http.StatusOK, response)
				return
			}
		}
	}

	// Fetch from DB
	response := buildConfigFromDB()

	// Cache in Redis
	if config.RedisClient != nil {
		data, _ := json.Marshal(response)
		config.RedisClient.Set(ctx, configCacheKey, data, configCacheTTL)
	}

	c.JSON(http.StatusOK, response)
}

func buildConfigFromDB() models.AppConfigResponse {
	var airports []models.Airport
	config.DB.Raw(`
		SELECT id, name, code, city, country, lat, lng 
		FROM airports 
		WHERE is_active = 1
		ORDER BY name
	`).Scan(&airports)

	var tierRows []models.VehicleTierRow
	if err := config.DB.Raw(`
		SELECT id, name, code, image, passengers, luggage, description, is_active 
		FROM vehicle_tiers 
		ORDER BY passengers
	`).Scan(&tierRows).Error; err != nil {
		slog.Error("scan vehicle_tiers", "error", err)
	}

	tiers := make([]models.VehicleTier, 0, len(tierRows))
	for _, r := range tierRows {
		tiers = append(tiers, models.VehicleTier{
			ID:          r.ID,
			Name:        r.Name,
			Code:        r.Code,
			Image:       r.Image,
			Passengers:  r.Passengers,
			Luggage:     r.Luggage,
			Description: r.Description,
			IsActive:    r.IsActive.Bool, // false if null/0, true if 1
		})
	}

	var rentalCars []models.RentalCarConfig // ADD THIS
	config.DB.Raw(`
		SELECT id, name, description, image_url, rent_per_day, passengers, luggage
		FROM rental_cars 
		WHERE is_active = 1
		ORDER BY rent_per_day ASC
	`).Scan(&rentalCars)

	configMap := make(map[string]string)
	rows, _ := config.DB.Raw(`SELECT config_key, config_value FROM app_config`).Rows()
	defer rows.Close()
	for rows.Next() {
		var key, value string
		rows.Scan(&key, &value)
		configMap[key] = value
	}

	return models.AppConfigResponse{
		Airports:             airports,
		VehicleTiers:         tiers,
		RentalCars:           rentalCars, // ADD THIS
		DeliveryFee:          parseFloat(configMap["delivery_fee"], 150.00),
		ProcessingFeePercent: parseFloat(configMap["processing_fee_percent"], 3.5),
		BaseFare:             parseFloat(configMap["base_fare"], 10.00),
		PricePerKm:           parseFloat(configMap["price_per_km"], 6.30),
		MinimumFare:          parseFloat(configMap["minimum_fare"], 18.00),
	}
}

func InvalidateConfigCache() {
	if config.RedisClient != nil {
		config.RedisClient.Del(context.Background(), configCacheKey)
	}
}

func parseFloat(val string, fallback float64) float64 {
	if val == "" {
		return fallback
	}
	var result float64
	if _, err := fmt.Sscanf(val, "%f", &result); err == nil {
		return result
	}
	return fallback
}

func toBool(v interface{}) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case []byte:
		return len(x) > 0 && x[0] != '0'
	case string:
		return x == "1" || x == "true"
	}
	return false
}
