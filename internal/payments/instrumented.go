package payments

import (
	"context"
	"errors"
	"strings"

	"github.com/itsakhash/payment-service/internal/metrics"
)

// CreatePayment runs createPayment and records its outcome as a metric.
func (s *Service) CreatePayment(ctx context.Context, req CreateRequest) (Result, error) {
	res, err := s.createPayment(ctx, req)
	metrics.Payments.WithLabelValues(outcome(res, err)).Inc()
	return res, err
}

func outcome(res Result, err error) string {
	switch {
	case err == nil && res.Replayed:
		return "replayed"
	case err == nil:
		return strings.ToLower(res.Status) // "succeeded" or "failed"
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrAccountNotFound), errors.Is(err, ErrKeyReused):
		return "rejected"
	default:
		return "error"
	}
}
