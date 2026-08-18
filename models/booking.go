package models

import "time"

type Booking struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SessionID      string    `gorm:"index;size:255" json:"session_id"`
	DriverID       string    `gorm:"index;size:36" json:"driver_id"`
	RiderID    string `json:"rider_id"`
    RiderEmail string `json:"rider_email"`
	PickupAddress  string    `json:"pickup_address"`
	DropoffAddress string    `json:"dropoff_address"`
	PickupLat      float64   `json:"pickup_lat"`
	PickupLng      float64   `json:"pickup_lng"`
	DropoffLat     float64   `json:"dropoff_lat"`
	DropoffLng     float64   `json:"dropoff_lng"`
	DistanceKm     float64   `json:"distance_km"`
	DurationText   string    `json:"duration_text"`
	FareLow        float64   `json:"fare_low"`
	FareHigh       float64   `json:"fare_high"`
	Status         string    `gorm:"default:pending" json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (Booking) TableName() string { return "bookings" }
