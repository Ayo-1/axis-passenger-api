package handlers

import (
	"context"
	"fmt"
	"log"
	"os"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

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

	log.Printf("[PUSH] Sending to device: %s...", deviceToken[:min(20, len(deviceToken))])

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
			Headers: map[string]string{
				"apns-priority": "10",
			},
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
			"title": "🚕 New Ride Request",
			"body":  fmt.Sprintf("From: %s\nTo: %s\nFare: %s", pickup, dropoff, fareRange),
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
