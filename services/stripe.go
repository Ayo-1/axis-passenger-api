package services

import (
	"fmt"
	"log/slog"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
)

type StripeService struct {
	secretKey string
}

func NewStripeService(secretKey string) *StripeService {
	stripe.Key = secretKey
	return &StripeService{secretKey: secretKey}
}

type StripePaymentLinkResponse struct {
	PaymentURL string `json:"payment_url"`
	SessionID  string `json:"session_id"`
}

func (s *StripeService) GeneratePaymentLink(ghsAmount float64, targetCurrency, email, description, reference string, metadata map[string]string) (*StripePaymentLinkResponse, error) {
	rate := getFXRate(targetCurrency)

	convertedAmount := ghsAmount * rate
	if convertedAmount < 0.50 {
		convertedAmount = 0.50
	}

	unitAmount := int64(convertedAmount * 100)

	params := &stripe.CheckoutSessionParams{
		Mode:          stripe.String(string(stripe.CheckoutSessionModePayment)),
		CustomerEmail: stripe.String(email),
		ClientReferenceID: stripe.String(reference),  // ADD THIS
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency:   stripe.String(targetCurrency),
					UnitAmount: stripe.Int64(unitAmount),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name: stripe.String(description),
					},
				},
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: stripe.String("https://your-site.com/payment/success?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:  stripe.String("https://your-site.com/payment/cancel"),
		Metadata:   metadata,
	}

	sess, err := session.New(params)
	if err != nil {
		slog.Error("stripe create session", "error", err)
		return nil, fmt.Errorf("failed to create payment session")
	}

	return &StripePaymentLinkResponse{
		PaymentURL: sess.URL,
		SessionID:  sess.ID,
	}, nil
}

func (s *StripeService) VerifySession(sessionID string) (bool, float64, error) {
	sess, err := session.Get(sessionID, nil)
	if err != nil {
		return false, 0, err
	}
	if sess.PaymentStatus == "paid" {
		amount := float64(sess.AmountTotal) / 100
		return true, amount, nil
	}
	return false, 0, nil
}

func getFXRate(targetCurrency string) float64 {
	fallbacks := map[string]float64{
		"USD": 0.065,
		"GBP": 0.051,
		"EUR": 0.060,
	}
	if rate, ok := fallbacks[targetCurrency]; ok {
		return rate
	}
	return 0.065
}