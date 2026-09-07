package models

import "time"


type HotelApplication struct {
	PropertyName       string `json:"propertyName" binding:"required"`
	ContactName        string `json:"contactName" binding:"required"`
	ContactPhone       string `json:"contactPhone" binding:"required"`
	WorkEmail          string `json:"workEmail" binding:"required,email"`
	RoomsTransfers     string `json:"roomsTransfers"`
	PreferredModel     string `json:"preferredModel" binding:"required,oneof=guest house"`
}


type Hotel struct {
	ID                    string  `gorm:"primaryKey" json:"id"`
	Name                  string  `json:"name"`
	City                  string  `json:"city"`
	ContactName           string  `json:"contact_name"`
	ContactEmail          string  `json:"contact_email"`
	ContactPhone          string  `json:"contact_phone"`
	FrontDeskContact      string  `json:"front_desk_contact"`
	RoomsMonthlyTransfers string  `json:"rooms_monthly_transfers"`
	PreferredModel        string  `json:"preferred_model"`
	CommissionRate        float64 `json:"commission_rate"`
	HouseAccountEnabled   bool    `json:"house_account_enabled"`
	PayoutMethod          string  `json:"payout_method"`
	PayoutDestination     string  `json:"payout_destination"`
	Status                string  `json:"status"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}


type HotelMember struct {
	ID       string `gorm:"primaryKey" json:"id"`
	HotelID  string `gorm:"column:hotel_id" json:"hotel_id"`
	Email    string `gorm:"column:email" json:"email"`
	PasswordHash    string     `json:"-"`
	GoogleID        string     `json:"google_id"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	FirstLoginAt    *time.Time `json:"first_login_at"`
	Role            string     `json:"role"`
	IsActive        bool       `json:"is_active"`
	CreatedAt       time.Time  `json:"created_at"`
}

type Commission struct {
	ID          string     `gorm:"primaryKey" json:"id"`
	BookingID   uint       `json:"booking_id"`
	HotelID     string     `json:"hotel_id"`
	Rate        float64    `json:"rate"`
	BaseAmount  float64    `json:"base_amount"`
	Amount      float64    `json:"amount"`
	Status      string     `json:"status"`
	AvailableAt *time.Time `json:"available_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type Payout struct {
	ID           string     `gorm:"primaryKey;size:36" json:"id"`
	HotelID      string     `gorm:"index;size:36" json:"hotel_id"`
	Amount       float64    `json:"amount"`
	Method       string     `gorm:"size:20;default:momo" json:"method"`
	Destination  string     `gorm:"size:255" json:"destination"`
	AccountName  string     `gorm:"size:255" json:"account_name"`
	BankName     string     `gorm:"size:255" json:"bank_name"`
	Network      string     `gorm:"size:50" json:"network"`
	Status       string     `gorm:"size:20;default:requested" json:"status"`
	RequestedAt  *time.Time `json:"requested_at"`
	ProcessedAt  *time.Time `json:"processed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// In models package
type PartnerPaymentToken struct {
	ID        string    `gorm:"primaryKey;size:36" json:"id"`
	BookingID uint      `gorm:"index" json:"booking_id"`
	Token     string    `gorm:"size:64;uniqueIndex" json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func (PartnerPaymentToken) TableName() string {
	return "partner_payment_tokens"
}
func (Payout) TableName() string { return "payouts" }
func (Commission) TableName() string { return "commissions" }
func (HotelMember) TableName() string { return "hotel_members" }
func (Hotel) TableName() string { return "hotels" }