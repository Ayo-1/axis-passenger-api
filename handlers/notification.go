package handlers

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"

	"goapi/config"
	"goapi/models"
	"goapi/services"
)

var notificationService = services.NewNotificationService()

var fcmClient *messaging.Client

func InitFCM() {
	credPath := os.Getenv("FIREBASE_CREDENTIALS_PATH_AXIS")
	if credPath == "" {
		log.Println("⚠️ FIREBASE_CREDENTIALS_PATH not set, push notifications disabled")
		return
	}

	opt := option.WithCredentialsFile(credPath)
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		log.Println("⚠️ FCM init failed:", err)
		return
	}

	fcmClient, err = app.Messaging(context.Background())
	if err != nil {
		log.Println("⚠️ FCM client failed:", err)
		return
	}
	log.Println("✅ FCM initialized")
}

// ── Quick ride notification (existing) ──

func SendBookingNotification(deviceToken string, bookingID uint, sessionId string, pickup, dropoff, fareRange string, pickupLat, pickupLng, dropoffLat, dropoffLng float64, distanceKm float64, durationText string, riderEmail string, riderId string) {
	log.Printf("[PUSH] Attempting to send notification for booking #%d", bookingID)

	if fcmClient == nil {
		log.Println("[PUSH] ❌ FCM client not initialized")
		return
	}

	if deviceToken == "" {
		log.Printf("[PUSH] ❌ No device token for booking #%d", bookingID)
		return
	}

	message := &messaging.Message{
		Notification: &messaging.Notification{
			Title: "🚕 New Ride Request",
			Body:  fmt.Sprintf("From: %s\nTo: %s\nFare: %s", pickup, dropoff, fareRange),
		},
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				Title:     "🚕 New Ride Request",
				Body:      fmt.Sprintf("From: %s\nTo: %s\nFare: %s", pickup, dropoff, fareRange),
				Sound:     "default",
				ChannelID: "booking_requests",
			},
		},
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{"apns-priority": "10"},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: "🚕 New Ride Request",
						Body:  fmt.Sprintf("From: %s\nTo: %s\nFare: %s", pickup, dropoff, fareRange),
					},
					Sound: "default",
				},
			},
		},
		Data: map[string]string{
			"title":        "🚕 New Ride Request",
			"body":         fmt.Sprintf("From: %s\nTo: %s\nFare: %s", pickup, dropoff, fareRange),
			"booking_id":   fmt.Sprint(bookingID),
			"session_id":   sessionId,
			"pickup":       pickup,
			"dropoff":      dropoff,
			"fare":         fareRange,
			"type":         "new_booking",
			"pickup_lat":   fmt.Sprintf("%.6f", pickupLat),
			"pickup_lng":   fmt.Sprintf("%.6f", pickupLng),
			"dropoff_lat":  fmt.Sprintf("%.6f", dropoffLat),
			"dropoff_lng":  fmt.Sprintf("%.6f", dropoffLng),
			"distance_km":  fmt.Sprintf("%.1f", distanceKm),
			"duration_min": durationText,
			"rider_email":  riderEmail,
			"rider_id":     riderId,
		},
		Token: deviceToken,
	}

	resp, err := fcmClient.Send(context.Background(), message)
	if err != nil {
		log.Printf("[PUSH] ❌ Failed for booking #%d: %v", bookingID, err)
	} else {
		log.Printf("[PUSH] ✅ Sent for booking #%d, MessageID: %s", bookingID, resp)
	}
}

// ── Scheduled ride notification ──

func NotifyDriver(booking models.BookingSchedule) {
	// Get driver details
	var driver struct {
		ID          string `db:"id"`
		Email       string `db:"email"`
		FirstName   string `db:"first_name"`
		DeviceToken string `db:"device_token"`
		Phone       string `db:"phone"`
	}
	
	err := config.DB.Raw(`
		SELECT id, email, first_name, device_token, phone 
		FROM drivers 
		WHERE id = ?
	`, booking.DriverID).Scan(&driver).Error

	if err != nil {
		log.Printf("[NOTIFY] ❌ Driver not found for booking #%d: %v", booking.ID, err)
		return
	}

	// Format date/time
		scheduledTime := ""
		if booking.ScheduledAt != nil {
			scheduledTime = booking.ScheduledAt.Format("Mon, Jan 2 at 3:04 PM")
		}

		tripDesc := fmt.Sprintf("%s → %s", booking.PickupAddress, booking.DropoffAddress)
		if booking.TripType == "both" && booking.ReturnPickupAddress != "" {
			tripDesc += fmt.Sprintf("\nReturn: %s → %s", booking.ReturnPickupAddress, booking.ReturnDropoffAddress)
		}

		notificationBody := fmt.Sprintf("%s\n%s\nFare: GHS %.2f", scheduledTime, tripDesc, booking.FareTotal)

		// ── 1. Create notification record in database ──
		if notificationService != nil {
			_, err := notificationService.CreateNotification(
				booking.DriverID,
				"new_scheduled_ride",
				"📅 New Scheduled Ride",
				notificationBody,
				map[string]interface{}{
					"booking_id":     booking.ID,
					"reference":      booking.SessionID,
					"scheduled_at":   scheduledTime,
					"pickup":         booking.PickupAddress,
					"dropoff":        booking.DropoffAddress,
					"return_pickup":  booking.ReturnPickupAddress,
					"return_dropoff": booking.ReturnDropoffAddress,
					"trip_type":      booking.TripType,
					"fare_total":     booking.FareTotal,
					"guest_name":     booking.GuestName,
					"guest_phone":    booking.GuestPhone,
					"passengers":     booking.Passengers,
					"luggage":        booking.Luggage,
					"flight_number":  booking.FlightNumber,
					"return_flight":  booking.ReturnFlightNumber,
				},
			)
			if err != nil {
				log.Printf("[NOTIFY] ❌ Failed to create notification record: %v", err)
			} else {
				log.Printf("[NOTIFY] ✅ Created notification record")
			}
		}


	// ── 1. FCM Push Notification ──
	if fcmClient != nil && driver.DeviceToken != "" {
		pushMessage := &messaging.Message{
			Notification: &messaging.Notification{
				Title: "📅 New Scheduled Ride",
				Body:  fmt.Sprintf("%s\n%s\nFare: GHS %.2f", scheduledTime, tripDesc, booking.FareTotal),
			},
			Android: &messaging.AndroidConfig{
				Priority: "high",
				Notification: &messaging.AndroidNotification{
					Title:     "📅 New Scheduled Ride",
					Body:      fmt.Sprintf("%s\n%s\nFare: GHS %.2f", scheduledTime, tripDesc, booking.FareTotal),
					Sound:     "default",
					ChannelID: "scheduled_rides",
				},
			},
			APNS: &messaging.APNSConfig{
				Headers: map[string]string{"apns-priority": "10"},
				Payload: &messaging.APNSPayload{
					Aps: &messaging.Aps{
						Alert: &messaging.ApsAlert{
							Title: "📅 New Scheduled Ride",
							Body:  fmt.Sprintf("%s\n%s\nFare: GHS %.2f", scheduledTime, tripDesc, booking.FareTotal),
						},
						Sound: "default",
					},
				},
			},
			Data: map[string]string{
				"type":          "scheduled_ride",
				"booking_id":    fmt.Sprint(booking.ID),
				"reference":     booking.SessionID,
				"scheduled_at":  scheduledTime,
				"pickup":        booking.PickupAddress,
				"dropoff":       booking.DropoffAddress,
				"return_pickup": booking.ReturnPickupAddress,
				"return_dropoff": booking.ReturnDropoffAddress,
				"trip_type":     booking.TripType,
				"fare_total":    fmt.Sprintf("%.2f", booking.FareTotal),
				"guest_name":    booking.GuestName,
				"guest_phone":   booking.GuestPhone,
				"passengers":    fmt.Sprint(booking.Passengers),
				"luggage":       fmt.Sprint(booking.Luggage),
				"flight_number": booking.FlightNumber,
				"return_flight": booking.ReturnFlightNumber,
			},
			Token: driver.DeviceToken,
		}

		resp, err := fcmClient.Send(context.Background(), pushMessage)
		if err != nil {
			log.Printf("[NOTIFY] ❌ Push failed for booking #%d: %v", booking.ID, err)
		} else {
			log.Printf("[NOTIFY] ✅ Push sent for booking #%d, MessageID: %s", booking.ID, resp)
		}
	} else {
		log.Printf("[NOTIFY] ⚠️ No device token for driver %s", driver.FirstName)
	}

	// ── 2. Email Notification ──
	if driver.Email != "" && emailService != nil {
		go func() {
			err := emailService.SendScheduledRideNotification(
				driver.Email, driver.FirstName, booking.SessionID, scheduledTime,
				booking.PickupAddress, booking.DropoffAddress,
				booking.ReturnPickupAddress, booking.ReturnDropoffAddress,
				booking.TripType, fmt.Sprintf("%.2f", booking.FareTotal),
				booking.GuestName, booking.GuestPhone, booking.FlightNumber,
				fmt.Sprint(booking.Passengers), fmt.Sprint(booking.Luggage),
			)
			if err != nil {
				log.Printf("[NOTIFY] ❌ Email failed for booking #%d: %v", booking.ID, err)
			} else {
				log.Printf("[NOTIFY] ✅ Email sent for booking #%d to %s", booking.ID, driver.Email)
			}
		}()
	}

	// Update driver status
	config.DB.Model(&models.BookingSchedule{}).
		Where("id = ?", booking.ID).
		Updates(map[string]interface{}{
			"driver_status":      "notified",
			"driver_notified_at": time.Now(),
		})
}

// ── Driver cancellation notification to rider ──

func notifyRiderDriverCancelled(booking models.BookingSchedule, reason string) {
	if booking.GuestEmail == "" {
		return
	}

	// Email rider
	if emailService != nil {
		go func() {
			err := emailService.SendDriverCancellationEmail(
				booking.GuestEmail, booking.GuestName, booking.SessionID, reason,
			)
			if err != nil {
				log.Printf("[NOTIFY] ❌ Rider cancellation email failed: %v", err)
			}
		}()
	}

	// SMS rider (optional — if you have SMS service)
	// ...
}

// ── Replacement driver notification ──

func notifyReplacementDriver(booking models.BookingSchedule) {
	NotifyDriver(booking)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}