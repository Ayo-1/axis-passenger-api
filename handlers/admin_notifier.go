package handlers

import (
	"fmt"
	"log"
	"os"
	"strings"

	"goapi/models"
	"goapi/services"
)

var adminSMSService *services.SMSService

func InitAdminSMS() {
	provider := os.Getenv("SMS_PROVIDER")

	var apiKey string
	switch provider {
	case "arkesel":
		apiKey = os.Getenv("ARKESEL_API_KEY")
	case "mnotify":
		apiKey = os.Getenv("MNOTIFY_API_KEY")
	}

	senderID := os.Getenv("SMS_SENDER_ID")
	if senderID == "" {
		senderID = "Axis"
	}

	if apiKey == "" {
		log.Println("⚠️ SMS API key not set - admin SMS disabled")
		return
	}

	adminSMSService = services.NewSMSService(provider, apiKey, senderID)
	log.Println("✅ Admin SMS service initialized")
}

// NotifyAdminNewBooking sends email + SMS to admin when a new booking is created
func NotifyAdminNewBooking(booking models.BookingSchedule) {
	adminEmail := os.Getenv("ADMIN_NOTIFICATION_EMAIL")
	adminPhones := os.Getenv("ADMIN_NOTIFICATION_PHONES") // comma-separated

	// Build booking summary
	tripLabel := map[string]string{
		"pickup":  "Airport pickup",
		"dropoff": "Airport drop-off",
		"both":    "Round trip",
		"rental":  "Rental",
	}[booking.TripType]
	if tripLabel == "" {
		tripLabel = booking.TripType
	}

	scheduledTime := ""
	if booking.ScheduledAt != nil {
		scheduledTime = booking.ScheduledAt.Format("Mon, Jan 2 at 3:04 PM")
	}

	// ── 1. Send email ──
	if adminEmail != "" && emailService != nil {
		go func() {
			err := emailService.SendAdminNewBookingNotification(
				adminEmail,
				booking.SessionID,
				booking.GuestName,
				booking.GuestPhone,
				booking.GuestEmail,
				tripLabel,
				booking.Airport,
				booking.PickupAddress,
				booking.DropoffAddress,
				scheduledTime,
				booking.FlightNumber,
				fmt.Sprintf("%d", booking.Passengers),
				fmt.Sprintf("%d", booking.Luggage),
				fmt.Sprintf("%.2f", booking.FareTotal),
				booking.PaymentMode,
				booking.Channel,
			)
			if err != nil {
				log.Printf("[ADMIN] ❌ Email failed: %v", err)
			} else {
				log.Printf("[ADMIN] ✅ Email sent for booking %s", booking.SessionID)
			}
		}()
	}

	// ── 2. Send SMS ──
	if adminSMSService != nil && adminPhones != "" {
		go func() {
			smsBody := fmt.Sprintf(
				"NEW BOOKING %s\n%s | %s\n%s\n%s -> %s\nGHS %.2f | %s",
				booking.SessionID,
				booking.GuestName,
				booking.GuestPhone,
				scheduledTime,
				shortAddr(booking.PickupAddress),
				shortAddr(booking.DropoffAddress),
				booking.FareTotal,
				booking.PaymentMode,
			)

			for _, phone := range strings.Split(adminPhones, ",") {
				phone = strings.TrimSpace(phone)
				if phone == "" {
					continue
				}
				if err := adminSMSService.Send(phone, smsBody); err != nil {
					log.Printf("[ADMIN] ❌ SMS failed for %s: %v", phone, err)
				} else {
					log.Printf("[ADMIN] ✅ SMS sent to %s", phone)
				}
			}
		}()
	}
}

// shortAddr keeps SMS messages under 160 chars — trims addresses
func shortAddr(addr string) string {
	if len(addr) <= 30 {
		return addr
	}
	return addr[:27] + "..."
}
