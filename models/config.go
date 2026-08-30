package models

type Airport struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Code    string  `json:"code"`
	City    string  `json:"city"`
	Country string  `json:"country"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

type VehicleTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	Image		string `json:"image"`
	Passengers  int    `json:"passengers"`
	Luggage     int    `json:"luggage"`
	Description string `json:"description"`
}

type AppConfigResponse struct {
	Airports             []Airport     `json:"airports"`
	VehicleTiers         []VehicleTier `json:"vehicle_tiers"`
	RentalCars           []RentalCarConfig   `json:"rental_cars"`  // ADD THIS
	DeliveryFee          float64       `json:"delivery_fee"`
	ProcessingFeePercent float64       `json:"processing_fee_percent"`
	BaseFare             float64       `json:"base_fare"`
	PricePerKm           float64       `json:"price_per_km"`
	MinimumFare          float64       `json:"minimum_fare"`
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