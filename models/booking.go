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

type BookingSchedule struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	SessionID        string     `gorm:"index;size:255" json:"session_id"`
	DriverID         string     `gorm:"index;size:36" json:"driver_id"`
	DriverStatus     string     `gorm:"size:20;default:pending" json:"driver_status"`
	DriverNotifiedAt *time.Time `json:"driver_notified_at"`
	DriverAcceptedAt *time.Time `json:"driver_accepted_at"`
	CancellationReason string   `gorm:"size:255" json:"cancellation_reason"`
	HotelID          *string    `gorm:"size:36" json:"hotel_id"`
	RiderID          *string    `gorm:"size:36" json:"rider_id"`
	RiderEmail       *string    `gorm:"size:255" json:"rider_email"`

	ServiceType        string     `gorm:"size:20;default:ride" json:"service_type"`
	Channel            string     `gorm:"size:20;default:direct" json:"channel"`
	GuestName          string     `gorm:"size:255" json:"guest_name"`
	GuestPhone         string     `gorm:"size:30" json:"guest_phone"`
	GuestEmail         string     `gorm:"size:255" json:"guest_email"`
	RoomNumber         string     `gorm:"size:50" json:"room_number"`
	TripType           string     `gorm:"size:20;default:pickup" json:"trip_type"`
	Airport            string     `gorm:"size:100" json:"airport"`
	FlightNumber       string     `gorm:"size:20" json:"flight_number"`
	ReturnFlightNumber string     `gorm:"size:20" json:"return_flight_number"`
	ScheduledAt        *time.Time `json:"scheduled_at"`
	ReturnScheduledAt  *time.Time `json:"return_scheduled_at"`

	// Outbound leg
	PickupAddress  string  `json:"pickup_address"`
	DropoffAddress string  `json:"dropoff_address"`
	PickupLat      float64 `json:"pickup_lat"`
	PickupLng      float64 `json:"pickup_lng"`
	DropoffLat     float64 `json:"dropoff_lat"`
	DropoffLng     float64 `json:"dropoff_lng"`

	// Return leg  ← add these
	ReturnPickupAddress  string  `json:"return_pickup_address"`
	ReturnPickupLat      float64 `json:"return_pickup_lat"`
	ReturnPickupLng      float64 `json:"return_pickup_lng"`
	ReturnDropoffAddress string  `json:"return_dropoff_address"`
	ReturnDropoffLat     float64 `json:"return_dropoff_lat"`
	ReturnDropoffLng     float64 `json:"return_dropoff_lng"`

	DistanceKm       float64 `json:"distance_km"`
	DurationText     string  `json:"duration_text"`
	FareLow          float64 `json:"fare_low"`
	FareHigh         float64 `json:"fare_high"`
	FareTotal        float64 `json:"fare_total"`
	ProcessingFee    float64 `gorm:"default:0" json:"processing_fee"`
	PartnerNet       float64 `json:"partner_net"`
	CommissionAmount float64 `json:"commission_amount"`
	FinalFare        float64 `json:"final_fare"`

	Passengers     int     `gorm:"default:1" json:"passengers"`
	Luggage        int     `gorm:"default:1" json:"luggage"`
	TierID         string  `gorm:"size:30" json:"tier_id"`
	RentalDays     int     `json:"rental_days"`
	DeliveryOption string  `gorm:"size:20" json:"delivery_option"`
	DeliveryFee    float64 `gorm:"default:0" json:"delivery_fee"`

	PaymentMode   string `gorm:"size:20;default:prepaid" json:"payment_mode"`
	PaymentStatus string `gorm:"size:20;default:pending" json:"payment_status"`
	Status        string `gorm:"size:50;default:pending" json:"status"`
	Notes         string `json:"notes"`

	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type RentalCar struct {
	ID          string  `gorm:"primaryKey" json:"id"`
	PartnerID   string  `json:"partner_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ImageURL    string  `json:"image_url"`
	RentPerDay  float64 `json:"rent_per_day"`
	Passengers  int     `json:"passengers"`
	Luggage     int     `json:"luggage"`
	IsActive    bool    `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
}

func (Booking) TableName() string { return "bookings" }
func (BookingSchedule) TableName() string { return "bookings" }
