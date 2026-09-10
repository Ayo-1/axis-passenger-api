// services/email.go
package services

import (
	"fmt"
	"log"
	"os"

	"github.com/resend/resend-go/v2"
)

type EmailService struct {
	client *resend.Client
}

func NewEmailService() *EmailService {
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		log.Println("Warning: RESEND_API_KEY not set")
		return &EmailService{client: nil}
	}

	client := resend.NewClient(apiKey)
	return &EmailService{client: client}
}

// ---------------------------------------------------------------------------
// OTP
// ---------------------------------------------------------------------------

type OTPEmailData struct {
	EmailHeader
	OTP           string
	ExpiryMinutes int
}

const otpBody = `
{{define "extra_style"}}
  .otp-wrap { text-align:center; }
  .card-label { font-size:12px; letter-spacing:.5px; text-transform:uppercase; color:#8a8a8a; font-weight:700; margin:0 0 14px 0; }
  .otp-box { display:inline-block; background:#ffffff; border-radius:10px; padding:16px 28px; box-shadow:0 2px 6px rgba(0,0,0,0.06); }
  .otp-code { font-size:36px; font-weight:800; letter-spacing:10px; color:#111; }
  .expiry { font-size:13px; color:#5f5f5f; margin:16px 0 0 0; }
  .expiry strong { color:#1f9d55; }
{{end}}
{{define "body"}}
<div class="card otp-wrap">
  <p class="card-label">Your Verification Code</p>
  <div class="otp-box"><span class="otp-code">{{.OTP}}</span></div>
  <p class="expiry">Expires in <strong>{{.ExpiryMinutes}} minutes</strong></p>
</div>
<div class="notice notice-warn">
  <p class="notice-body">If you didn't request this code, you can safely ignore this email — your account is still secure.</p>
</div>
{{end}}
`

func (e *EmailService) SendOTPEmail(to, otp string) error {
	if e.client == nil {
		log.Printf("Would send OTP %s to %s (email disabled)", otp, to)
		return nil
	}

	brand := DefaultBrand()
	data := OTPEmailData{
		EmailHeader: EmailHeader{
			Brand:       brand,
			Eyebrow:     "Login to your dashboard",
			HeaderTitle: "Use the code below to sign in",
			Subtitle:    "",
		},
		OTP:           otp,
		ExpiryMinutes: 10,
	}

	htmlContent, err := renderEmail(otpBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{to},
		Subject: fmt.Sprintf("Your OTP for %s", brand.AppName),
		Html:    htmlContent,
	}

	if _, err := e.client.Emails.Send(params); err != nil {
		log.Printf("Failed to send OTP email: %v", err)
		return err
	}

	log.Printf("OTP email sent to %s", to)
	return nil
}

// ---------------------------------------------------------------------------
// Booking confirmation
// ---------------------------------------------------------------------------

type BookingConfirmationEmailData struct {
	EmailHeader
	BookingID      string
	Status         string
	PickupAddress  string
	DropoffAddress string
	DateTime       string
	FareRange      string
	MapImageURL    string
	NoticeText     string
}

const bookingConfirmationBody = `
{{define "body"}}
<div class="card">
  <table width="100%" cellpadding="0" cellspacing="0">
    <tr>
      <td>
        <p class="label">Booking Code</p>
        <p class="code">ID #{{.BookingID}}</p>
      </td>
      <td align="right"><span class="badge">&#9679; {{.Status}}</span></td>
    </tr>
  </table>
  <div class="divider"></div>
  <table width="100%" cellpadding="0" cellspacing="0">
    <tr>
      <td width="48%">
        <div class="box">
          <p class="label">Estimated Fare</p>
          <p class="value">{{.FareRange}}</p>
        </div>
      </td>
      <td width="4%"></td>
      <td width="48%">
        <div class="box">
          <p class="label">Date &amp; Time</p>
          <p class="value">{{.DateTime}}</p>
        </div>
      </td>
    </tr>
  </table>

  <img class="map" src="{{.MapImageURL}}" alt="Route map">

  <div class="stop">
    <span class="dot dot-pickup"></span>
    <p class="stop-time">Pickup</p>
    <p class="stop-addr">{{.PickupAddress}}</p>
  </div>
  <div class="stop">
    <span class="dot dot-drop"></span>
    <p class="stop-time">Destination</p>
    <p class="stop-addr">{{.DropoffAddress}}</p>
  </div>
</div>

<div class="notice">
  <p class="notice-title">Important Notice</p>
  <p class="notice-body">{{.NoticeText}}</p>
</div>
{{end}}
`

// SendBookingConfirmation sends a booking confirmation email.
// mapURL: build this with buildMapURL(pickupLat, pickupLng, dropLat, dropLng)
// before calling this function — it needs real coordinates, not a brand asset.
func (e *EmailService) SendBookingConfirmation(to, bookingID, pickup, dropoff, date, fareRange, mapURL string) error {
	if e.client == nil {
		log.Printf("Would send booking confirmation to %s (email disabled)", to)
		return nil
	}

	data := BookingConfirmationEmailData{
		EmailHeader: EmailHeader{
			Brand:       DefaultBrand(),
			Eyebrow:     "Booking Confirmed!",
			HeaderTitle: "Your ride is secured.",
			Subtitle:    "Your ride has been confirmed. Here are your booking details:",
		},
		BookingID:      bookingID,
		Status:         "CONFIRMED",
		PickupAddress:  pickup,
		DropoffAddress: dropoff,
		DateTime:       date,
		FareRange:      fareRange,
		MapImageURL:    mapURL,
		NoticeText:     "Your driver will meet you at the pickup location. Keep your app open to track them as they approach.",
	}

	htmlContent, err := renderEmail(bookingConfirmationBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{to},
		Subject: fmt.Sprintf("Booking Confirmation #%s", bookingID),
		Html:    htmlContent,
	}

	_, err = e.client.Emails.Send(params)
	return err
}

// ---------------------------------------------------------------------------
// Hotel application confirmation (to the hotel)
// ---------------------------------------------------------------------------

type HotelApplicationConfirmationData struct {
	EmailHeader
	PropertyName string
	ContactName  string
}

const hotelApplicationConfirmationBody = `
{{define "body"}}
<div class="card">
  <p>Hello {{.ContactName}},</p>
  <p>
    Thank you for applying on behalf of <strong>{{.PropertyName}}</strong>.
    We have your details and our partnerships team will review them shortly.
  </p>
  <p>
    You'll hear from us within one business day with next steps or any questions we have.
    Submitting an application does not guarantee approval.
  </p>
</div>

<div class="notice">
  <p class="notice-title">What happens next</p>
  <p class="notice-body">
    We verify property details, set up staff logins, and agree the preferred billing model
    (guest-pays or house account). You'll receive a separate email once your account is ready.
  </p>
</div>
{{end}}
`

// SendHotelApplicationConfirmation emails the hotel confirming we received their application.
func (e *EmailService) SendHotelApplicationConfirmation(to, propertyName, contactName string) error {
	if e.client == nil {
		log.Printf("Would send hotel application confirmation to %s (email disabled)", to)
		return nil
	}

	brand := DefaultBrand()
	data := HotelApplicationConfirmationData{
		EmailHeader: EmailHeader{
			Brand:       brand,
			Eyebrow:     "Hotel partners",
			HeaderTitle: "We received your application",
			Subtitle:    fmt.Sprintf("Thanks for applying to partner with %s.", brand.AppName),
		},
		PropertyName: propertyName,
		ContactName:  contactName,
	}

	htmlContent, err := renderEmail(hotelApplicationConfirmationBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{to},
		Subject: fmt.Sprintf("We received your application — %s", brand.AppName),
		Html:    htmlContent,
	}

	if _, err := e.client.Emails.Send(params); err != nil {
		log.Printf("Failed to send hotel application confirmation to %s: %v", to, err)
		return err
	}

	log.Printf("Hotel application confirmation sent to %s", to)
	return nil
}

// ---------------------------------------------------------------------------
// New hotel application notification (internal, to the Axis team)
// ---------------------------------------------------------------------------

type NewHotelApplicationNotificationData struct {
	EmailHeader
	PropertyName   string
	ContactName    string
	ContactEmail   string
	ContactPhone   string
	RoomsTransfers string
	PreferredModel string
}

const newHotelApplicationNotificationBody = `
{{define "body"}}
<div class="card">
  <p class="label">Property</p>
  <p class="value">{{.PropertyName}}</p>
  <div class="divider"></div>

  <p class="label">Primary contact</p>
  <p class="value">{{.ContactName}}</p>

  <p class="label">Work email</p>
  <p class="value">{{.ContactEmail}}</p>

  <p class="label">Phone</p>
  <p class="value">{{.ContactPhone}}</p>

  <p class="label">Rooms / monthly transfers</p>
  <p class="value">{{.RoomsTransfers}}</p>

  <p class="label">Preferred model</p>
  <p class="value">{{.PreferredModel}}</p>
</div>
{{end}}
`

// SendNewHotelApplicationNotification notifies the Axis team of a new application.
func (e *EmailService) SendNewHotelApplicationNotification(
	propertyName, contactName, contactEmail, contactPhone, roomsTransfers, preferredModel string,
) error {
	if e.client == nil {
		log.Println("Would send new hotel application notification (email disabled)")
		return nil
	}

	adminEmail := os.Getenv("HOTEL_APPLICATION_EMAIL")
	if adminEmail == "" {
		log.Println("Warning: HOTEL_APPLICATION_EMAIL not set — skipping admin notification")
		return nil
	}

	brand := DefaultBrand()
	data := NewHotelApplicationNotificationData{
		EmailHeader: EmailHeader{
			Brand:       brand,
			Eyebrow:     "Internal · Partners",
			HeaderTitle: "New hotel application",
			Subtitle:    fmt.Sprintf("%s just applied to partner with %s.", propertyName, brand.AppName),
		},
		PropertyName:   propertyName,
		ContactName:    contactName,
		ContactEmail:   contactEmail,
		ContactPhone:   contactPhone,
		RoomsTransfers: roomsTransfers,
		PreferredModel: preferredModel,
	}

	htmlContent, err := renderEmail(newHotelApplicationNotificationBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{adminEmail},
		Subject: fmt.Sprintf("New hotel application: %s", propertyName),
		Html:    htmlContent,
	}

	if _, err := e.client.Emails.Send(params); err != nil {
		log.Printf("Failed to send new hotel application notification: %v", err)
		return err
	}

	log.Printf("New hotel application notification sent to %s", adminEmail)
	return nil
}

// ---------------------------------------------------------------------------
// Scheduled ride notification (to the driver)
// ---------------------------------------------------------------------------

type ScheduledRideNotificationData struct {
	EmailHeader
	Reference     string
	ScheduledTime string
	Pickup        string
	Dropoff       string
	ReturnPickup  string
	ReturnDropoff string
	TripType      string
	FareTotal     string
	GuestName     string
	GuestPhone    string
	FlightNumber  string
	Passengers    string
	Luggage       string
	HasReturn     bool
}

const scheduledRideNotificationBody = `
{{define "body"}}
<div class="card">
  <table width="100%" cellpadding="0" cellspacing="0">
    <tr>
      <td>
        <p class="label">Booking reference</p>
        <p class="value" style="margin-bottom:0;">{{.Reference}}</p>
      </td>
      <td align="right"><span class="badge">ASSIGNED</span></td>
    </tr>
  </table>
  <div class="divider"></div>

  <p class="label">When</p>
  <p class="value">{{.ScheduledTime}}</p>

  <div class="stop">
    <p class="stop-time">Pickup</p>
    <p class="stop-addr">{{.Pickup}}</p>
  </div>
  <div class="stop" style="margin-bottom:16px;">
    <p class="stop-time">Drop-off</p>
    <p class="stop-addr">{{.Dropoff}}</p>
  </div>

  {{if .HasReturn}}
  <div class="divider"></div>
  <p class="label">Return leg</p>
  <div class="stop">
    <p class="stop-time">Return pickup</p>
    <p class="stop-addr">{{.ReturnPickup}}</p>
  </div>
  <div class="stop" style="margin-bottom:16px;">
    <p class="stop-time">Return drop-off</p>
    <p class="stop-addr">{{.ReturnDropoff}}</p>
  </div>
  {{end}}

  <div class="divider"></div>
  <table width="100%" cellpadding="0" cellspacing="0">
    <tr>
      <td width="48%">
        <p class="label">Guest</p>
        <p class="value">{{.GuestName}}</p>
      </td>
      <td width="4%"></td>
      <td width="48%">
        <p class="label">Phone</p>
        <p class="value">{{.GuestPhone}}</p>
      </td>
    </tr>
    <tr>
      <td width="48%">
        <p class="label">Passengers / bags</p>
        <p class="value">{{.Passengers}} / {{.Luggage}}</p>
      </td>
      <td width="4%"></td>
      <td width="48%">
        <p class="label">Fare</p>
        <p class="value">GHS {{.FareTotal}}</p>
      </td>
    </tr>
    {{if .FlightNumber}}
    <tr>
      <td colspan="3">
        <p class="label">Flight</p>
        <p class="value" style="margin-bottom:0;">{{.FlightNumber}}</p>
      </td>
    </tr>
    {{end}}
  </table>
</div>

<div class="notice">
  <p class="notice-title">Before you go</p>
  <p class="notice-body">
    Open the Axis driver app for navigation and guest contact. If the flight is tracked,
    pickup time may adjust automatically — you'll get a push if it changes.
  </p>
</div>
{{end}}
`

// SendScheduledRideNotification emails the driver about a newly confirmed scheduled ride.
func (e *EmailService) SendScheduledRideNotification(
	toEmail, driverName, reference, scheduledTime,
	pickup, dropoff, returnPickup, returnDropoff, tripType,
	fareTotal, guestName, guestPhone, flightNumber, passengers, luggage string,
) error {
	if e.client == nil {
		log.Printf("Would send scheduled ride notification to %s (email disabled)", toEmail)
		return nil
	}

	brand := DefaultBrand()
	data := ScheduledRideNotificationData{
		EmailHeader: EmailHeader{
			Brand:       brand,
			Eyebrow:     "Scheduled transfer",
			HeaderTitle: "New ride assigned to you",
			Subtitle:    fmt.Sprintf("Hi %s, a guest transfer is confirmed for %s.", driverName, scheduledTime),
		},
		Reference:     reference,
		ScheduledTime: scheduledTime,
		Pickup:        pickup,
		Dropoff:       dropoff,
		ReturnPickup:  returnPickup,
		ReturnDropoff: returnDropoff,
		TripType:      tripType,
		FareTotal:     fareTotal,
		GuestName:     guestName,
		GuestPhone:    guestPhone,
		FlightNumber:  flightNumber,
		Passengers:    passengers,
		Luggage:       luggage,
		HasReturn:     tripType == "both" && returnPickup != "",
	}

	htmlContent, err := renderEmail(scheduledRideNotificationBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{toEmail},
		Subject: fmt.Sprintf("New scheduled ride %s — %s", reference, brand.AppName),
		Html:    htmlContent,
	}

	if _, err := e.client.Emails.Send(params); err != nil {
		log.Printf("Failed to send scheduled ride notification to %s: %v", toEmail, err)
		return err
	}

	log.Printf("Scheduled ride notification sent to %s", toEmail)
	return nil
}

// ---------------------------------------------------------------------------
// Driver cancellation (to the guest)
// ---------------------------------------------------------------------------

type DriverCancellationEmailData struct {
	EmailHeader
	GuestName string
	Reference string
	Reason    string
}

const driverCancellationBody = `
{{define "body"}}
<div class="card">
  <p>Hello {{.GuestName}},</p>
  <p>
    The driver assigned to booking <span class="ref">{{.Reference}}</span> is no longer available
    {{if .Reason}}({{.Reason}}){{end}}.
  </p>
  <p>
    We're matching you with another EV driver for the same time and route.
    You'll get a confirmation with the new driver's details shortly — no action needed from you.
  </p>
  <p>
    If anything else changes, or you need to reschedule, reply to this email or use your
    manage booking link.
  </p>
</div>

<div class="notice notice-warn">
  <p class="notice-title">Your fare is protected</p>
  <p class="notice-body">
    The price you already paid stays the same. Flight-change cover still applies on airport legs.
  </p>
</div>
{{end}}
`

// SendDriverCancellationEmail emails the guest that their assigned driver cancelled.
func (e *EmailService) SendDriverCancellationEmail(toEmail, guestName, reference, reason string) error {
	if e.client == nil {
		log.Printf("Would send driver cancellation email to %s (email disabled)", toEmail)
		return nil
	}

	brand := DefaultBrand()
	data := DriverCancellationEmailData{
		EmailHeader: EmailHeader{
			Brand:       brand,
			Eyebrow:     "Booking update",
			HeaderTitle: "Your driver had to cancel",
			Subtitle:    "We're assigning a replacement so your transfer stays on track.",
		},
		GuestName: guestName,
		Reference: reference,
		Reason:    reason,
	}

	htmlContent, err := renderEmail(driverCancellationBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{toEmail},
		Subject: fmt.Sprintf("Update on your booking %s — %s", reference, brand.AppName),
		Html:    htmlContent,
	}

	if _, err := e.client.Emails.Send(params); err != nil {
		log.Printf("Failed to send driver cancellation email to %s: %v", toEmail, err)
		return err
	}

	log.Printf("Driver cancellation email sent to %s", toEmail)
	return nil
}

// ---------------------------------------------------------------------------
// Partner payment link email (to the guest)
// ---------------------------------------------------------------------------

type PartnerPaymentLinkEmailData struct {
	EmailHeader
	GuestName  string
	Reference  string
	PaymentURL string
	FareTotal  string
	HotelName  string
}

const partnerPaymentLinkBody = `
{{define "body"}}
<div class="card">
  <p>Hello {{.GuestName}},</p>
  <p>
    {{.HotelName}} has arranged an airport transfer for you through Axis.
    Your booking reference is <span class="ref">{{.Reference}}</span>.
  </p>
  <p>
    To confirm your ride, please complete payment of <strong>GHS {{.FareTotal}}</strong>
    using the button below.
  </p>
  <div class="button-wrap" style="text-align:center; margin:24px 0;">
    <a href="{{.PaymentURL}}" class="button">Complete Payment</a>
  </div>
  <p>
    You can choose to pay with Mobile Money (Paystack) or International Card (Stripe)
    on the payment page.
  </p>
</div>

<div class="notice">
  <p class="notice-title">Need to change anything?</p>
  <p class="notice-body">
    Reply to this email or contact {{.HotelName}} directly if your flight details change.
  </p>
</div>
{{end}}
`

// SendPartnerPaymentLinkEmail emails the guest a payment selection link.
func (e *EmailService) SendPartnerPaymentLinkEmail(
	toEmail, guestName, reference, paymentURL, fareTotal string,
) error {
	if e.client == nil {
		log.Printf("Would send partner payment link to %s (email disabled)", toEmail)
		return nil
	}

	brand := DefaultBrand()
	data := PartnerPaymentLinkEmailData{
		EmailHeader: EmailHeader{
			Brand:       brand,
			Eyebrow:     "Complete your booking",
			HeaderTitle: "Your airport transfer is ready",
			Subtitle:    "Complete payment to confirm your ride.",
		},
		GuestName:  guestName,
		Reference:  reference,
		PaymentURL: paymentURL,
		FareTotal:  fareTotal,
		HotelName:  "Your Hotel",
	}

	htmlContent, err := renderEmail(partnerPaymentLinkBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{toEmail},
		Subject: fmt.Sprintf("Complete payment for your transfer %s — %s", reference, brand.AppName),
		Html:    htmlContent,
	}

	if _, err := e.client.Emails.Send(params); err != nil {
		log.Printf("Failed to send partner payment link to %s: %v", toEmail, err)
		return err
	}

	log.Printf("Partner payment link sent to %s", toEmail)
	return nil
}

// ---------------------------------------------------------------------------
// Booking confirmation email (to the guest after payment)
// ---------------------------------------------------------------------------

type GuestBookingConfirmationData struct {
	EmailHeader
	Reference      string
	GuestName      string
	TripType       string
	Airport        string
	PickupAddress  string
	DropoffAddress string
	DateTime       string
	ReturnDateTime string
	FlightNumber   string
	ReturnFlight   string
	Passengers     string
	Luggage        string
	VehicleTier    string
	FareTotal      string
	PaymentMethod  string
	DriverName     string
	DriverPhone    string
	CarModel       string
	PlateNumber    string
	HasReturn      bool
	HasDriver      bool
	ManageURL      string
}

const guestBookingConfirmationBody = `
{{define "body"}}
<div class="card">
  <table width="100%" cellpadding="0" cellspacing="0">
    <tr>
      <td>
        <p class="label">Booking Reference</p>
        <p class="code">{{.Reference}}</p>
      </td>
      <td align="right"><span class="badge">&#9679; CONFIRMED</span></td>
    </tr>
  </table>
  <div class="divider"></div>

  <p>Hello {{.GuestName}},</p>
  <p>Your airport transfer is confirmed and paid in full.</p>

  <div class="divider"></div>

  <p class="label">Trip Details</p>
  <table width="100%" cellpadding="0" cellspacing="0">
    <tr>
      <td width="48%">
        <p class="label">Service</p>
        <p class="value">{{.TripType}}</p>
      </td>
      <td width="4%"></td>
      <td width="48%">
        <p class="label">Airport</p>
        <p class="value">{{.Airport}}</p>
      </td>
    </tr>
    <tr>
      <td width="48%">
        <p class="label">Date & Time</p>
        <p class="value">{{.DateTime}}</p>
      </td>
      <td width="4%"></td>
      <td width="48%">
        <p class="label">Passengers / Bags</p>
        <p class="value">{{.Passengers}} / {{.Luggage}}</p>
      </td>
    </tr>
    {{if .FlightNumber}}
    <tr>
      <td colspan="3">
        <p class="label">Flight Number</p>
        <p class="value" style="margin-bottom:0;">{{.FlightNumber}}</p>
      </td>
    </tr>
    {{end}}
  </table>

  <div class="divider"></div>

  <div class="stop">
    <span class="dot dot-pickup"></span>
    <p class="stop-time">Pickup</p>
    <p class="stop-addr">{{.PickupAddress}}</p>
  </div>
  <div class="stop">
    <span class="dot dot-drop"></span>
    <p class="stop-time">Drop-off</p>
    <p class="stop-addr">{{.DropoffAddress}}</p>
  </div>

  {{if .HasReturn}}
  <div class="divider"></div>
  <p class="label">Return Trip</p>
  <div class="stop">
    <span class="dot dot-pickup"></span>
    <p class="stop-time">Return pickup</p>
    <p class="stop-addr">{{.DropoffAddress}}</p>
  </div>
  <div class="stop">
    <span class="dot dot-drop"></span>
    <p class="stop-time">Return drop-off</p>
    <p class="stop-addr">{{.PickupAddress}}</p>
  </div>
  <p class="label">Return Date & Time</p>
  <p class="value">{{.ReturnDateTime}}</p>
  {{if .ReturnFlight}}
  <p class="label">Return Flight</p>
  <p class="value">{{.ReturnFlight}}</p>
  {{end}}
  {{end}}

  {{if .HasDriver}}
  <div class="divider"></div>
  <p class="label">Your Driver</p>
  <p class="value">{{.DriverName}}</p>
  <p class="value">{{.CarModel}} · {{.PlateNumber}}</p>
  <p class="value">Phone: {{.DriverPhone}}</p>
  {{end}}

  <div class="divider"></div>
  <table width="100%" cellpadding="0" cellspacing="0">
    <tr>
      <td width="48%">
        <p class="label">Vehicle</p>
        <p class="value">{{.VehicleTier}}</p>
      </td>
      <td width="4%"></td>
      <td width="48%">
        <p class="label">Total Paid</p>
        <p class="value">GHS {{.FareTotal}}</p>
      </td>
    </tr>
    <tr>
      <td colspan="3">
        <p class="label">Payment Method</p>
        <p class="value" style="margin-bottom:0;">{{.PaymentMethod}}</p>
      </td>
    </tr>
  </table>
</div>

<div class="notice">
  <p class="notice-title">Manage your booking</p>
  <p class="notice-body">
    Need to update your flight or cancel? Use the manage booking page:
    <br>
    <a href="{{.ManageURL}}" style="color:#1f9d55; font-weight:600;">Manage booking →</a>
  </p>
</div>

<div class="notice notice-warn">
  <p class="notice-title">Cancellation Policy</p>
  <p class="notice-body">
    Free changes or cancellation up to 12 hours before pickup — full refund.
    Inside 12 hours: 50% of the fare is retained. No-show: fare charged in full.
    Verified flight cancellation: no penalty, ever.
  </p>
</div>
{{end}}
`

// SendGuestBookingConfirmation emails the guest their confirmed booking details.
func (e *EmailService) SendGuestBookingConfirmation(
	toEmail, guestName, reference, tripType, airport,
	pickupAddress, dropoffAddress, dateTime, returnDateTime,
	flightNumber, returnFlight, passengers, luggage,
	vehicleTier, fareTotal, paymentMethod,
	driverName, driverPhone, carModel, plateNumber string,
	hasReturn, hasDriver bool,
) error {
	if e.client == nil {
		log.Printf("Would send booking confirmation to %s (email disabled)", toEmail)
		return nil
	}

	brand := DefaultBrand()
	data := GuestBookingConfirmationData{
		EmailHeader: EmailHeader{
			Brand:       brand,
			Eyebrow:     "Booking Confirmed",
			HeaderTitle: "Your ride is secured.",
			Subtitle:    "Here are your booking details:",
		},
		Reference:      reference,
		GuestName:      guestName,
		TripType:       tripType,
		Airport:        airport,
		PickupAddress:  pickupAddress,
		DropoffAddress: dropoffAddress,
		DateTime:       dateTime,
		ReturnDateTime: returnDateTime,
		FlightNumber:   flightNumber,
		ReturnFlight:   returnFlight,
		Passengers:     passengers,
		Luggage:        luggage,
		VehicleTier:    vehicleTier,
		FareTotal:      fareTotal,
		PaymentMethod:  paymentMethod,
		DriverName:     driverName,
		DriverPhone:    driverPhone,
		CarModel:       carModel,
		PlateNumber:    plateNumber,
		HasReturn:      hasReturn,
		HasDriver:      hasDriver,
		ManageURL:      os.Getenv("APP_URL_MAIN") + "/manage?ref=" + reference,
	}

	htmlContent, err := renderEmail(guestBookingConfirmationBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{toEmail},
		Subject: fmt.Sprintf("Booking Confirmed: %s — %s", reference, brand.AppName),
		Html:    htmlContent,
	}

	if _, err := e.client.Emails.Send(params); err != nil {
		log.Printf("Failed to send booking confirmation to %s: %v", toEmail, err)
		return err
	}

	log.Printf("Booking confirmation sent to %s", toEmail)
	return nil
}

type AdminNewBookingData struct {
	EmailHeader
	Reference      string
	GuestName      string
	GuestPhone     string
	GuestEmail     string
	TripLabel      string
	Airport        string
	PickupAddress  string
	DropoffAddress string
	ScheduledTime  string
	FlightNumber   string
	Passengers     string
	Luggage        string
	FareTotal      string
	PaymentMode    string
	Channel        string
}

const adminNewBookingBody = `
{{define "body"}}
<div class="card">
  <p class="label">New booking received</p>
  <p class="code">{{.Reference}}</p>
  <div class="divider"></div>

  <p class="label">Guest</p>
  <p class="value">{{.GuestName}}</p>
  <p class="value">{{.GuestPhone}} · {{.GuestEmail}}</p>

  <div class="divider"></div>

  <p class="label">Trip</p>
  <p class="value">{{.TripLabel}} — {{.Airport}}</p>

  <div class="stop">
    <p class="stop-time">Pickup</p>
    <p class="stop-addr">{{.PickupAddress}}</p>
  </div>
  <div class="stop" style="margin-bottom:16px;">
    <p class="stop-time">Drop-off</p>
    <p class="stop-addr">{{.DropoffAddress}}</p>
  </div>

  <p class="label">When</p>
  <p class="value">{{.ScheduledTime}}</p>

  {{if .FlightNumber}}
  <p class="label">Flight</p>
  <p class="value">{{.FlightNumber}}</p>
  {{end}}

  <p class="label">Passengers / Bags</p>
  <p class="value">{{.Passengers}} / {{.Luggage}}</p>

  <div class="divider"></div>

  <p class="label">Fare</p>
  <p class="value">GHS {{.FareTotal}}</p>

  <p class="label">Payment</p>
  <p class="value">{{.PaymentMode}} · Channel: {{.Channel}}</p>
</div>
{{end}}
`

func (e *EmailService) SendAdminNewBookingNotification(
	toEmail, reference, guestName, guestPhone, guestEmail,
	tripLabel, airport, pickup, dropoff, scheduledTime,
	flightNumber, passengers, luggage, fareTotal, paymentMode, channel string,
) error {
	if e.client == nil {
		log.Printf("Would send admin notification for %s (email disabled)", reference)
		return nil
	}

	brand := DefaultBrand()
	data := AdminNewBookingData{
		EmailHeader: EmailHeader{
			Brand:       brand,
			Eyebrow:     "Internal · Bookings",
			HeaderTitle: "New booking received",
			Subtitle:    "A guest just booked through the " + channel + " channel.",
		},
		Reference:      reference,
		GuestName:      guestName,
		GuestPhone:     guestPhone,
		GuestEmail:     guestEmail,
		TripLabel:      tripLabel,
		Airport:        airport,
		PickupAddress:  pickup,
		DropoffAddress: dropoff,
		ScheduledTime:  scheduledTime,
		FlightNumber:   flightNumber,
		Passengers:     passengers,
		Luggage:        luggage,
		FareTotal:      fareTotal,
		PaymentMode:    paymentMode,
		Channel:        channel,
	}

	html, err := renderEmail(adminNewBookingBody, data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{toEmail},
		Subject: fmt.Sprintf("New booking %s — %s", reference, guestName),
		Html:    html,
	}

	_, err = e.client.Emails.Send(params)
	return err
}
