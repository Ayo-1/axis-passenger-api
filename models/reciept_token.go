// models/receipt_token.go
package models

import "time"

type ReceiptToken struct {
	ID        string     `gorm:"primaryKey;size:36" json:"id"`
	BookingID uint       `gorm:"index" json:"booking_id"`
	Reference string     `gorm:"size:255" json:"reference"`
	TokenHash string     `gorm:"size:64;uniqueIndex" json:"token_hash"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func (ReceiptToken) TableName() string {
	return "receipt_tokens"
}