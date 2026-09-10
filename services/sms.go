package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type SMSService struct {
	provider string
	apiKey   string
	senderID string
	client   *http.Client
}

func NewSMSService(provider, apiKey, senderID string) *SMSService {
	return &SMSService{
		provider: provider,
		apiKey:   apiKey,
		senderID: senderID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *SMSService) Send(phone, message string) error {
	switch s.provider {
	case "arkesel":
		return s.sendArkesel(phone, message)
	case "mnotify":
		return s.sendMNotify(phone, message)
	default:
		return fmt.Errorf("unknown SMS provider: %s", s.provider)
	}
}

// ── Arkesel ──
type arkeselRequest struct {
	Sender     string   `json:"sender"`
	Recipients []string `json:"recipients"`
	Message    string   `json:"message"`
}

type arkeselResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (s *SMSService) sendArkesel(phone, message string) error {
	phone = stripPrefix(phone)

	body := arkeselRequest{
		Sender:     s.senderID,
		Recipients: []string{phone},
		Message:    message,
	}

	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", "https://sms.arkesel.com/api/v2/sms/send", bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		slog.Error("arkesel send", "error", err, "phone", phone)
		return fmt.Errorf("SMS send failed")
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var arResp arkeselResponse
	json.Unmarshal(respBody, &arResp)

	if arResp.Status != "success" && arResp.Status != "ok" {
		slog.Error("arkesel response", "status", arResp.Status, "message", arResp.Message, "phone", phone)
		return fmt.Errorf("SMS send failed: %s", arResp.Message)
	}

	slog.Info("SMS sent via Arkesel", "phone", maskPhone(phone))
	return nil
}

// ── MNotify ──
type mNotifyRequest struct {
	Recipient    []string `json:"recipient"`
	Message      string   `json:"message"`
	Sender       string   `json:"sender"`
	IsSchedule   bool     `json:"is_schedule"`
	ScheduleDate string   `json:"schedule_date,omitempty"`
}

type mNotifyResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (s *SMSService) sendMNotify(phone, message string) error {
	body := mNotifyRequest{
		Recipient:  []string{phone},
		Message:    message,
		Sender:     s.senderID,
		IsSchedule: false,
	}

	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", "https://api.mnotify.com/api/sms/quick", bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "key "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		slog.Error("mnotify send", "error", err, "phone", phone)
		return fmt.Errorf("SMS send failed")
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var mnResp mNotifyResponse
	json.Unmarshal(respBody, &mnResp)

	if mnResp.Status != "success" && mnResp.Status != "ok" {
		slog.Error("mnotify response", "status", mnResp.Status, "message", mnResp.Message, "phone", phone)
		return fmt.Errorf("SMS send failed: %s", mnResp.Message)
	}

	slog.Info("SMS sent via MNotify", "phone", maskPhone(phone))
	return nil
}

// ── Helpers ──
func stripPrefix(phone string) string {
	if len(phone) > 0 && phone[0] == '+' {
		return phone[1:]
	}
	return phone
}

func maskPhone(phone string) string {
	if len(phone) < 6 {
		return "***"
	}
	return phone[:3] + "***" + phone[len(phone)-3:]
}
