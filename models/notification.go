package models

import (
	"time"
)

type DriverNotification struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	DriverID  string    `gorm:"index;size:36" json:"driver_id"`
	Type      string    `gorm:"column:type;size:50;default:general" json:"type"`  // Explicitly specify column name
	Title     string    `gorm:"size:255" json:"title"`
	Message   string    `gorm:"type:text" json:"message"`
	Data      string    `gorm:"type:json" json:"data"`
	IsRead    bool      `gorm:"default:false" json:"is_read"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (DriverNotification) TableName() string {
	return "notifications"
}