package models

import "time"

// RentalKYC holds the driver's identity and licence details collected with a
// self-drive rental. Reviewed manually by admins; there is no approval flow.
// Document columns hold private storage keys, never URLs.
type RentalKYC struct {
	ID              string     `gorm:"column:id;type:char(36);primaryKey" json:"id"`
	BookingID       uint       `gorm:"column:booking_id;type:bigint unsigned;not null;index" json:"booking_id"`
	IDType          string     `gorm:"column:id_type;type:varchar(16)" json:"id_type"`
	IDNumber        string     `gorm:"column:id_number;type:varchar(40)" json:"id_number"`
	IDExpiry        *time.Time `gorm:"column:id_expiry;type:date" json:"id_expiry"`
	Nationality     string     `gorm:"column:nationality;type:varchar(60)" json:"nationality"`
	DateOfBirth     *time.Time `gorm:"column:date_of_birth;type:date" json:"date_of_birth"`
	HomeAddress     string     `gorm:"column:home_address;type:varchar(255)" json:"home_address"`
	ArrivalDate     *time.Time `gorm:"column:arrival_date;type:date" json:"arrival_date"`
	LicenceNumber   string     `gorm:"column:licence_number;type:varchar(40)" json:"licence_number"`
	LicenceCountry  string     `gorm:"column:licence_country;type:varchar(60)" json:"licence_country"`
	LicenceExpiry   *time.Time `gorm:"column:licence_expiry;type:date" json:"licence_expiry"`
	IDFrontKey      *string    `gorm:"column:id_front_key;type:varchar(160)" json:"-"`
	IDBackKey       *string    `gorm:"column:id_back_key;type:varchar(160)" json:"-"`
	LicenceFrontKey *string    `gorm:"column:licence_front_key;type:varchar(160)" json:"-"`
	LicenceBackKey  *string    `gorm:"column:licence_back_key;type:varchar(160)" json:"-"`
	SelfieKey       *string    `gorm:"column:selfie_key;type:varchar(160)" json:"-"`
	CreatedAt       time.Time  `gorm:"column:created_at;type:datetime;default:CURRENT_TIMESTAMP" json:"created_at"`
}

func (RentalKYC) TableName() string { return "rental_kyc" }
