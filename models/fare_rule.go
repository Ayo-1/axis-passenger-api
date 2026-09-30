package models

import "time"

// FareRule is the pricing for one vehicle tier. tier_id = "default" is the
// fallback for tiers without their own row. Edited from the admin panel;
// the API picks up changes within a minute.
type FareRule struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TierID       string    `gorm:"column:tier_id;type:varchar(36);not null;uniqueIndex" json:"tier_id"`
	BaseFare     float64   `gorm:"column:base_fare;type:decimal(10,2);not null;default:0" json:"base_fare"`
	PerKm        float64   `gorm:"column:per_km;type:decimal(10,2);not null;default:0" json:"per_km"`
	PerMinute    float64   `gorm:"column:per_minute;type:decimal(10,2);not null;default:0" json:"per_minute"`
	MinimumFare  float64   `gorm:"column:minimum_fare;type:decimal(10,2);not null;default:0" json:"minimum_fare"`
	PerPassenger float64   `gorm:"column:per_passenger;type:decimal(10,2);not null;default:0" json:"per_passenger"`
	PerLuggage   float64   `gorm:"column:per_luggage;type:decimal(10,2);not null;default:0" json:"per_luggage"`
	PlatformFee  float64   `gorm:"column:platform_fee;type:decimal(10,2);not null;default:0" json:"platform_fee"`
	RoundTo      float64   `gorm:"column:round_to;type:decimal(10,2);not null;default:1" json:"round_to"` // total rounded to nearest (1 = whole cedi)
	IsActive     bool      `gorm:"column:is_active;not null;default:true" json:"is_active"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (FareRule) TableName() string { return "fare_rules" }

const DefaultFareRuleTier = "default"
