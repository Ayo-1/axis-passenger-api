// services/distance.go
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"

	"goapi/config"
)

type RouteInfo struct {
	DistanceKm   float64 `json:"distance_km"`
	DistanceText string  `json:"distance_text"`
	DurationText string  `json:"duration_text"`
}

type distanceMatrixResponse struct {
	Rows []struct {
		Elements []struct {
			Status   string `json:"status"`
			Distance struct {
				Value int    `json:"value"`
				Text  string `json:"text"`
			} `json:"distance"`
			Duration struct {
				Text string `json:"text"`
			} `json:"duration"`
		} `json:"elements"`
	} `json:"rows"`
	Status string `json:"status"`
}

func roundCoord(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func distanceCacheKey(lat1, lng1, lat2, lng2 float64) string {
	return fmt.Sprintf("route:%.3f,%.3f:%.3f,%.3f", roundCoord(lat1), roundCoord(lng1), roundCoord(lat2), roundCoord(lng2))
}

// GetRouteInfo returns real driving distance/duration between two points, cached in
// Redis (routes don't change) to control Google Maps billing across quick and
// scheduled bookings alike.
func GetRouteInfo(lat1, lng1, lat2, lng2 float64) (RouteInfo, error) {
	ctx := context.Background()
	key := distanceCacheKey(lat1, lng1, lat2, lng2)

	if config.RedisClient != nil {
		if cached, err := config.RedisClient.Get(ctx, key).Result(); err == nil {
			var info RouteInfo
			if json.Unmarshal([]byte(cached), &info) == nil {
				return info, nil
			}
		}
	}

	info, err := fetchGoogleRouteInfo(lat1, lng1, lat2, lng2)
	if err != nil {
		// Google unavailable — pad straight-line distance for road detour rather
		// than failing the request outright.
		km := haversineKm(lat1, lng1, lat2, lng2) * 1.4
		return RouteInfo{DistanceKm: km, DistanceText: fmt.Sprintf("%.1f km", km)}, nil
	}

	if config.RedisClient != nil {
		if data, err := json.Marshal(info); err == nil {
			// Roads/addresses rarely change — cache for 30 days.
			config.RedisClient.Set(ctx, key, data, 30*24*time.Hour)
		}
	}

	return info, nil
}

// DrivingDistanceKm is a convenience wrapper for callers that only need the km figure
// (e.g. fare calculation), without the display text.
func DrivingDistanceKm(lat1, lng1, lat2, lng2 float64) (float64, error) {
	info, err := GetRouteInfo(lat1, lng1, lat2, lng2)
	return info.DistanceKm, err
}

func fetchGoogleRouteInfo(lat1, lng1, lat2, lng2 float64) (RouteInfo, error) {
	apiKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	if apiKey == "" {
		return RouteInfo{}, fmt.Errorf("GOOGLE_MAPS_API_KEY not set")
	}

	url := fmt.Sprintf(
		"https://maps.googleapis.com/maps/api/distancematrix/json?origins=%f,%f&destinations=%f,%f&key=%s&units=metric",
		lat1, lng1, lat2, lng2, apiKey,
	)

	client := http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return RouteInfo{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return RouteInfo{}, err
	}

	var parsed distanceMatrixResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return RouteInfo{}, err
	}

	if parsed.Status != "OK" || len(parsed.Rows) == 0 || len(parsed.Rows[0].Elements) == 0 ||
		parsed.Rows[0].Elements[0].Status != "OK" {
		return RouteInfo{}, fmt.Errorf("distance matrix returned no route")
	}

	el := parsed.Rows[0].Elements[0]
	return RouteInfo{
		DistanceKm:   float64(el.Distance.Value) / 1000.0,
		DistanceText: el.Distance.Text,
		DurationText: el.Duration.Text,
	}, nil
}

func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}
