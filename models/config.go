package models

import (
	"database/sql"
)

type Airport struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Code    string  `json:"code"`
	City    string  `json:"city"`
	Country string  `json:"country"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

// models
type VehicleTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	Image       string `json:"image"`
	Passengers  int    `json:"passengers"`
	Luggage     int    `json:"luggage"`
	Description string `json:"description"`
	IsActive    bool   `json:"is_active"`
}

type VehicleTierRow struct {
	ID          string       `gorm:"column:id"`
	Name        string       `gorm:"column:name"`
	Code        string       `gorm:"column:code"`
	Image       string       `gorm:"column:image"`
	Passengers  int          `gorm:"column:passengers"`
	Luggage     int          `gorm:"column:luggage"`
	Description string       `gorm:"column:description"`
	IsActive    sql.NullBool `gorm:"column:is_active"`
}

type AppConfigResponse struct {
	Airports             []Airport         `json:"airports"`
	VehicleTiers         []VehicleTier     `json:"vehicle_tiers"`
	RentalCars           []RentalCarConfig `json:"rental_cars"` // ADD THIS
	DeliveryFee          float64           `json:"delivery_fee"`
	ProcessingFeePercent float64           `json:"processing_fee_percent"`
	BaseFare             float64           `json:"base_fare"`
	PricePerKm           float64           `json:"price_per_km"`
	MinimumFare          float64           `json:"minimum_fare"`
}

type RentalCarConfig struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ImageURL    string  `json:"image_url"`
	RentPerDay  float64 `json:"rent_per_day"`
	Passengers  int     `json:"passengers"`
	Luggage     int     `json:"luggage"`
}

func (RentalCarConfig) TableName() string {
	return "rental_cars"
}
