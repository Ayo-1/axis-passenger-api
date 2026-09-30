package web

import (
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"goapi/config"
	"goapi/models"
	"goapi/services"
)

// ── Fare rules ───────────────────────────────────────────────
//
// Pricing lives in fare_rules, one row per vehicle tier plus a "default" row
// for tiers without their own. The admin panel edits the table directly;
// rules are re-read at most every fareRuleTTL, so changes apply within a
// minute without a redeploy.

const fareRuleTTL = time.Minute

var fareRules struct {
	sync.Mutex
	byTier   map[string]models.FareRule
	loadedAt time.Time
}

func loadFareRules() map[string]models.FareRule {
	fareRules.Lock()
	defer fareRules.Unlock()

	if fareRules.byTier != nil && time.Since(fareRules.loadedAt) < fareRuleTTL {
		return fareRules.byTier
	}

	var rows []models.FareRule
	if err := config.DB.Where("is_active = 1").Find(&rows).Error; err != nil {
		slog.Error("load fare rules", "error", err)
		if fareRules.byTier != nil {
			return fareRules.byTier // keep serving the last good set
		}
		return map[string]models.FareRule{}
	}

	byTier := make(map[string]models.FareRule, len(rows))
	for _, r := range rows {
		byTier[r.TierID] = r
	}
	fareRules.byTier = byTier
	fareRules.loadedAt = time.Now()
	return byTier
}

// fareRuleFor returns the tier's rule, falling back to the default row.
func fareRuleFor(tierID string) (models.FareRule, error) {
	rules := loadFareRules()
	if r, ok := rules[tierID]; ok && tierID != "" {
		return r, nil
	}
	if r, ok := rules[models.DefaultFareRuleTier]; ok {
		return r, nil
	}
	return models.FareRule{}, fmt.Errorf("no fare rule for tier %q and no default rule", tierID)
}

// SeedFareRules fills an empty fare_rules table from today's app_config
// values (plus the old hardcoded GHS 28 platform fee), with a row per vehicle
// tier, so prices don't change until someone edits a rule.
func SeedFareRules() {
	var count int64
	if err := config.DB.Model(&models.FareRule{}).Count(&count).Error; err != nil || count > 0 {
		return
	}

	var legacy struct {
		BaseFare    float64 `gorm:"column:base_fare"`
		PricePerKm  float64 `gorm:"column:price_per_km"`
		MinimumFare float64 `gorm:"column:minimum_fare"`
	}
	config.DB.Raw(`
		SELECT
			COALESCE(MAX(CASE WHEN config_key = 'base_fare' THEN CAST(config_value AS DECIMAL(10,2)) END), 0) as base_fare,
			COALESCE(MAX(CASE WHEN config_key = 'price_per_km' THEN CAST(config_value AS DECIMAL(10,2)) END), 0) as price_per_km,
			COALESCE(MAX(CASE WHEN config_key = 'minimum_fare' THEN CAST(config_value AS DECIMAL(10,2)) END), 0) as minimum_fare
		FROM app_config
		WHERE config_key IN ('base_fare','price_per_km','minimum_fare')
	`).Scan(&legacy)

	base := models.FareRule{
		BaseFare:    legacy.BaseFare,
		PerKm:       legacy.PricePerKm,
		MinimumFare: legacy.MinimumFare,
		PlatformFee: 28,
		RoundTo:     1,
		IsActive:    true,
	}

	var tierIDs []string
	config.DB.Raw(`SELECT id FROM vehicle_tiers`).Scan(&tierIDs)

	rows := []models.FareRule{}
	for _, id := range append([]string{models.DefaultFareRuleTier}, tierIDs...) {
		r := base
		r.TierID = id
		rows = append(rows, r)
	}
	if err := config.DB.Create(&rows).Error; err != nil {
		slog.Error("seed fare rules", "error", err)
		return
	}
	slog.Info("seeded fare rules from app_config", "rows", len(rows),
		"base_fare", base.BaseFare, "per_km", base.PerKm, "minimum_fare", base.MinimumFare)
}

// ── Fare calculation ─────────────────────────────────────────

type fareInput struct {
	AirportID            string
	TierID               string
	TripType             string
	MainLat, MainLng     float64
	ReturnLat, ReturnLng float64
	Passengers           int
	Luggage              int
	Protocol             bool
}

type fareBreakdown struct {
	BaseFare      float64 `json:"baseFare"`
	DistanceFare  float64 `json:"distanceFare"`
	TimeFare      float64 `json:"timeFare"`
	MinimumTopUp  float64 `json:"minimumTopUp"` // added when the trip is below the minimum fare
	PassengerFee  float64 `json:"passengerFee"`
	LuggageFee    float64 `json:"luggageFee"`
	PlatformFee   float64 `json:"platformFee"`
	ProtocolFee   float64 `json:"protocolFee"`
	Rounding      float64 `json:"rounding"`
	DistanceKm    float64 `json:"distanceKm"`
	DurationMin   float64 `json:"durationMin"`
	RouteEstimate bool    `json:"routeEstimate"` // true if Google was unavailable
}

type fareResult struct {
	Total       float64
	BaseFare    float64 // trip fare: base + distance + time (after minimum)
	ExtrasTotal float64 // passengers, luggage, platform and protocol fees
	PlatformFee float64
	ProtocolFee float64
	DistanceKm  float64
	Breakdown   fareBreakdown
}

// computeFare is the single source of truth for ride prices: the estimate,
// direct bookings and hotel bookings all call it.
//
//	trip  = base + km × per_km + minutes × per_minute   (at least minimum_fare)
//	total = trip + passengers × per_passenger + luggage × per_luggage
//	        + platform_fee + protocol, rounded to round_to
//
// A round trip ("both") sums both legs' distance and time, with one base fare.
func computeFare(in fareInput) (fareResult, error) {
	rule, err := fareRuleFor(in.TierID)
	if err != nil {
		return fareResult{}, err
	}

	var airport struct{ Lat, Lng float64 }
	if err := config.DB.Raw(`SELECT lat, lng FROM airports WHERE id = ? AND is_active = 1`, in.AirportID).Scan(&airport).Error; err != nil {
		return fareResult{}, err
	}
	if airport.Lat == 0 && airport.Lng == 0 {
		return fareResult{}, fmt.Errorf("invalid airport ID")
	}

	if math.Abs(in.MainLat) > 90 || math.Abs(in.MainLng) > 180 ||
		math.Abs(in.ReturnLat) > 90 || math.Abs(in.ReturnLng) > 180 {
		return fareResult{}, fmt.Errorf("invalid coordinates")
	}

	const maxLegKm = 800.0
	var legs [][4]float64
	switch in.TripType {
	case "pickup":
		legs = append(legs, [4]float64{airport.Lat, airport.Lng, in.MainLat, in.MainLng})
	case "dropoff":
		legs = append(legs, [4]float64{in.MainLat, in.MainLng, airport.Lat, airport.Lng})
	case "both":
		rLat, rLng := in.MainLat, in.MainLng
		if in.ReturnLat != 0 && in.ReturnLng != 0 {
			rLat, rLng = in.ReturnLat, in.ReturnLng
		}
		legs = append(legs,
			[4]float64{airport.Lat, airport.Lng, in.MainLat, in.MainLng},
			[4]float64{rLat, rLng, airport.Lat, airport.Lng})
	default:
		return fareResult{}, fmt.Errorf("invalid trip type")
	}

	var distanceKm, durationSec float64
	estimated := false
	for _, l := range legs {
		route, err := services.GetRouteInfo(l[0], l[1], l[2], l[3])
		if err != nil {
			return fareResult{}, fmt.Errorf("could not calculate route distance")
		}
		if route.DistanceKm > maxLegKm {
			return fareResult{}, fmt.Errorf("distance %.1fkm exceeds service area", route.DistanceKm)
		}
		distanceKm += route.DistanceKm
		durationSec += float64(route.DurationSec)
		estimated = estimated || route.Estimated
	}

	distanceKm = math.Round(distanceKm*10) / 10
	durationMin := math.Round(durationSec/60*10) / 10

	b := fareBreakdown{
		BaseFare:      rule.BaseFare,
		DistanceFare:  round2(distanceKm * rule.PerKm),
		TimeFare:      round2(durationMin * rule.PerMinute),
		PassengerFee:  round2(float64(in.Passengers) * rule.PerPassenger),
		LuggageFee:    round2(float64(in.Luggage) * rule.PerLuggage),
		PlatformFee:   rule.PlatformFee,
		DistanceKm:    distanceKm,
		DurationMin:   durationMin,
		RouteEstimate: estimated,
	}
	if in.Protocol {
		b.ProtocolFee = float64(in.Passengers) * 500
	}

	trip := b.BaseFare + b.DistanceFare + b.TimeFare
	if trip < rule.MinimumFare {
		b.MinimumTopUp = round2(rule.MinimumFare - trip)
		trip = rule.MinimumFare
	}
	trip = round2(trip)

	extras := round2(b.PassengerFee + b.LuggageFee + b.PlatformFee + b.ProtocolFee)
	raw := trip + extras

	step := rule.RoundTo
	if step <= 0 {
		step = 1
	}
	total := math.Round(raw/step) * step
	b.Rounding = round2(total - raw)

	return fareResult{
		Total:       round2(total),
		BaseFare:    trip,
		ExtrasTotal: extras,
		PlatformFee: b.PlatformFee,
		ProtocolFee: b.ProtocolFee,
		DistanceKm:  distanceKm,
		Breakdown:   b,
	}, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
