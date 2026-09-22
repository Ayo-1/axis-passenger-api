package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"io"
	"net/http"
	"time"
)

type PaystackService struct {
	secretKey string
	baseURL   string
	client    *http.Client
}

func NewPaystackService(secretKey string) *PaystackService {
	return &PaystackService{
		secretKey: secretKey,
		baseURL:   "https://api.paystack.co",
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

type PaymentLinkRequest struct {
	Amount      float64                `json:"amount"`
	Email       string                 `json:"email"`
	Description string                 `json:"description"`
	Reference   string                 `json:"reference"`
	Metadata    map[string]interface{} `json:"metadata"`
	CallbackURL string                 `json:"callback_url,omitempty"`
}

type PaymentLinkResponse struct {
	PaymentURL string `json:"payment_url"`
	Reference  string `json:"reference"`
}

func (s *PaystackService) GeneratePaymentLink(req PaymentLinkRequest) (*PaymentLinkResponse, error) {
	body := map[string]interface{}{
		"email":     req.Email,
		"amount":    int(req.Amount * 100),
		"currency":  "GHS",
		"reference": req.Reference,
		"channels":  []string{"card", "mobile_money", "bank_transfer"},
		"metadata":  req.Metadata,
	}

	if req.CallbackURL != "" {
		body["callback_url"] = req.CallbackURL
	}

	resp, err := s.post("/transaction/initialize", body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
		Data    struct {
			AuthorizationURL string `json:"authorization_url"`
			Reference        string `json:"reference"`
		} `json:"data"`
	}
	json.Unmarshal(resp, &result)

	if !result.Status {
		return nil, fmt.Errorf(result.Message)
	}

	return &PaymentLinkResponse{
		PaymentURL: result.Data.AuthorizationURL,
		Reference:  result.Data.Reference,
	}, nil
}

func (s *PaystackService) VerifyTransaction(reference string) (bool, string, float64, error) {
	req, err := http.NewRequest("GET", "https://api.paystack.co/transaction/verify/"+url.PathEscape(reference), nil)
	if err != nil {
		return false, "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.secretKey)

	res, err := s.client.Do(req)
	if err != nil {
		return false, "", 0, err
	}
	defer res.Body.Close()

	var apiResp struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
		Data    struct {
			Status   string  `json:"status"`
			Amount   float64 `json:"amount"`   // in pesewas
			Currency string  `json:"currency"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&apiResp); err != nil {
		return false, "", 0, err
	}

	// Paystack returns HTTP 200 with status:false for unknown references
	if !apiResp.Status {
		return false, "", 0, nil
	}

	// Log what Paystack actually said so this can never silently lie again
	log.Printf("[PAYSTACK VERIFY] ref=%s http=%d status=%v data.status=%q amount=%.2f %s",
		reference, res.StatusCode, apiResp.Status, apiResp.Data.Status,
		apiResp.Data.Amount/100, apiResp.Data.Currency)

	// Only "success" is paid. "pending", "abandoned", "failed", "reversed" are NOT paid.
	if apiResp.Data.Status != "success" {
		return false, apiResp.Data.Status, 0, nil
	}

	// Paystack amounts are in pesewas — convert to GHS
	amount := apiResp.Data.Amount / 100
	return true, "success", amount, nil
}

func (s *PaystackService) get(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", s.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.secretKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (s *PaystackService) post(path string, body interface{}) ([]byte, error) {
	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", s.baseURL+path, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.secretKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}