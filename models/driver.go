package models

import "time"

type DriverData struct {
	ID          uint   `gorm:"primaryKey"`
	UserID      uint   `json:"user_id"`
	CarMake     string `json:"car_make"`
	CarModel    string `json:"car_model"`
	Year        string `json:"year"`
	Bay         string `json:"bay"`
	PlateNumber string `json:"plate_number"`
}

func (DriverData) TableName() string { return "driver_data" }

type DriverAssignment struct {
	ID         uint      `gorm:"primaryKey"`
	DriverID   string    `gorm:"index;size:36"`
	SessionID  string    `gorm:"index;size:255"`
	AssignedAt time.Time
	ExpiresAt  time.Time
}

func (DriverAssignment) TableName() string { return "driver_assignments" }

// This is what gets returned to the frontend
type DriverResponse struct {
	ID          string `json:"id"`
	DriverNumber int `json:"driver_number"`
	Username    string `json:"username"`
	Phone       string `json:"phone"`
	Avatar      string `json:"avatar"`
	CarMake     string `json:"car_make"`
	CarModel    string `json:"car_model"`
	Year        string `json:"year"`
	Bay         string `json:"bay"`
	PlateNumber string `json:"plate_number"`
}
