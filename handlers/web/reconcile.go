package web

import (
	"log/slog"
	"time"

	"goapi/config"
	"goapi/models"
)

func StartPaymentReconciler() {
	go func() {
		slog.Info("payment reconciler started")
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			reconcilePendingIntents()
		}
	}()
}

func reconcilePendingIntents() {
	var intents []models.PaymentIntent
	if err := config.DB.
		Where("status = 'pending' AND created_at BETWEEN ? AND ?",
			time.Now().Add(-24*time.Hour), time.Now().Add(-3*time.Minute)).
		Order("created_at ASC").Limit(50).Find(&intents).Error; err != nil {
		slog.Error("reconcile: load intents", "error", err)
		return
	}

	for _, in := range intents {
		var success bool
		var paid float64
		var err error

		switch in.Provider {
		case "paystack":
			success, _, paid, err = PaystackService.VerifyTransaction(in.ProviderRef)
		case "stripe":
			success, paid, err = StripeService.VerifySession(in.ProviderRef)
		default:
			continue
		}

		if err != nil {
			slog.Error("reconcile: verify", "ref", in.Reference, "error", err)
			continue
		}
		if !success {
			continue
		}
		if paid+0.01 < in.ExpectedAmount {
			slog.Error("reconcile: underpayment",
				"ref", in.Reference, "paid", paid, "expected", in.ExpectedAmount)
			continue
		}

		if err := confirmPayment(in, in.Provider); err != nil {
			slog.Error("reconcile: confirm", "ref", in.Reference, "error", err)
			continue
		}
		slog.Info("reconcile: payment confirmed", "ref", in.Reference, "provider", in.Provider)
	}
}
