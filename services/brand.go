package services

import "time"

// Brand holds the asset URLs you'll host yourself (S3/R2/CDN) and reuse
// across every email template. Embed this into each data struct via
// composition so templates can reference {{.LogoURL}} etc. directly.
type Brand struct {
	CompanyName          string
	AppName			string
	LogoURL              string
	HeaderIllustrationURL string // the car/curved-shape header graphic
	FooterPatternURL     string // tiled background pattern for footer
	CompanyAddressLine1  string
	CompanyAddressLine2  string
	FacebookURL          string
	InstagramURL         string
	LinkedInURL          string
	FacebookIconURL      string
	InstagramIconURL     string
	LinkedInIconURL      string
	Year                 int
}

func DefaultBrand() Brand {
	return Brand{
		CompanyName:            "Seesail Technologies Ltd.",
		AppName:            	"Axis",
		LogoURL:                "https://axis-assets.axisghana.com/axis_logo.png",
		HeaderIllustrationURL:  "https://axis-assets.axisghana.com/axs.png",
		FooterPatternURL:       "https://axis-assets.axisghana.com/axis_footer_pattern.png",
		CompanyAddressLine1:    "No. F170 Florida, House 6",
		CompanyAddressLine2:    "Third Labone Link, Accra",
		FacebookURL:            "https://www.tiktok.com/@getseesail/",
		InstagramURL:           "https://www.instagram.com/getseesail/",
		LinkedInURL:            "https://www.linkedin.com/company/seesail/",
		FacebookIconURL:        "https://axis-assets.axisghana.com/axis_facebook.png",
		InstagramIconURL:       "https://axis-assets.axisghana.com/axis_instagram.png",
		LinkedInIconURL:        "https://axis-assets.axisghana.com/axis_linkedin.png",
		Year:                   time.Now().Year(),
	}
}

// ── Booking confirmation ─────────────────────────────────────

type BookingConfirmationData struct {
	Brand
	BookingID       string
	Status          string // "CONFIRMED"
	FareRange       string
	DateTime        string
	PickupAddress   string
	DropoffAddress  string
	MapImageURL     string
	NoticeText      string
}

// ── Trip receipt (new) ───────────────────────────────────────

type TripReceiptData struct {
	Brand
	RiderName           string
	TimeOfDay           string // "this morning" / "this evening"
	Distance            string
	EstimatedFare       string
	EstimatedTime       string
	PaymentMethod       string // "CASH"
	PaymentMethodLabel  string // "Cash Payment"
	PaymentDate         string
	DriverName          string
	DriverAvatarURL     string
	PickupTime          string
	PickupAddress       string
	DropoffTime         string
	DropoffAddress      string
	MapImageURL         string
}

// ── OTP (revamped) ───────────────────────────────────────────

type OTPData struct {
	Brand
	Title         string
	Body          string
	OTP           string
	ExpiryMinutes int
	DeviceInfo    string
	IPAddress     string
	Timestamp     string
}