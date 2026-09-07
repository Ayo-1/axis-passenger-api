package services

import (
	"encoding/json"
	"fmt"

	"goapi/config"
	"goapi/models"
)

type NotificationService struct{}

func NewNotificationService() *NotificationService {
	return &NotificationService{}
}

// CreateNotification creates a notification record in the database
func (s *NotificationService) CreateNotification(driverID, notifType, title, message string, data map[string]interface{}) (*models.DriverNotification, error) {
	// Convert data map to JSON string
	dataJSON := "{}"
	if data != nil {
		if bytes, err := json.Marshal(data); err == nil {
			dataJSON = string(bytes)
		}
	}

	notification := &models.DriverNotification{
		DriverID: driverID,
		Type:     notifType,
		Title:    title,
		Message:  message,
		Data:     dataJSON,
		IsRead:   false,
	}

	if err := config.DB.Create(notification).Error; err != nil {
		return nil, fmt.Errorf("failed to create notification: %w", err)
	}

	return notification, nil
}

// CreateRideReminderNotification creates a reminder notification
func (s *NotificationService) CreateRideReminderNotification(booking models.BookingSchedule, minutesUntil int) error {
	scheduledTime := ""
	if booking.ScheduledAt != nil {
		scheduledTime = booking.ScheduledAt.Format("3:04 PM")
	}

	message := fmt.Sprintf("Your airport pickup is scheduled for %s. ✈️ %s · %s",
		scheduledTime, booking.Airport, booking.FlightNumber)

	_, err := s.CreateNotification(
		booking.DriverID,
		"ride_reminder",
		"Ride starting soon",
		message,
		map[string]interface{}{
			"booking_id":    booking.ID,
			"reference":     booking.SessionID,
			"minutes_until": minutesUntil,
			"scheduled_at":  scheduledTime,
		},
	)

	return err
}

// CreateFlightUpdateNotification creates notification for flight updates
func (s *NotificationService) CreateFlightUpdateNotification(booking models.BookingSchedule, newArrival, newPickup string) error {
	message := fmt.Sprintf("%s · %s\nNew arrival: %s → Pickup: %s",
		booking.Airport, booking.FlightNumber, newArrival, newPickup)

	_, err := s.CreateNotification(
		booking.DriverID,
		"flight_update",
		"Flight information updated",
		message,
		map[string]interface{}{
			"booking_id":    booking.ID,
			"reference":     booking.SessionID,
			"flight_number": booking.FlightNumber,
			"new_arrival":   newArrival,
			"new_pickup":    newPickup,
		},
	)

	return err
}