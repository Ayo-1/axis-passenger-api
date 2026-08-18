// models/rider.go
package models

import (
	"time"
)

type Rider struct {
	ID           string     `gorm:"primaryKey;type:char(36)" json:"id"`
	Email        string     `gorm:"uniqueIndex;size:255;not null" json:"email"`
	Phone        string     `gorm:"size:20" json:"phone,omitempty"`
	FirstName    string     `gorm:"size:100" json:"first_name,omitempty"`
	LastName     string     `gorm:"size:100" json:"last_name,omitempty"`
	Country      string     `gorm:"size:50" json:"country,omitempty"`
	AvatarURL    string     `gorm:"size:500" json:"avatar_url,omitempty"` // From Google
	GoogleID     string     `gorm:"index;size:255" json:"google_id,omitempty"`
	IsVerified   bool       `gorm:"default:false" json:"is_verified"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (Rider) TableName() string { return "riders" }