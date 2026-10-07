package payments

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
)

var ErrPaymentNotFound = errors.New("payment not found")

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Payment is the read-side view of a stored payment.
type Payment struct {
	PaymentID string
	Status    string
}

// GetPayment looks up a payment by id, using the cache when one is configured.
// Malformed ids return ErrInvalidRequest; unknown ids return ErrPaymentNotFound.
func (s *Service) GetPayment(ctx context.Context, id string) (Payment, error) {
	if !uuidRe.MatchString(id) {
		return Payment{}, ErrInvalidRequest
	}

	if s.cache != nil {
		if p, ok := s.cache.Get(ctx, id); ok {
			return p, nil
		}
	}

	var p Payment
	err := s.pool.QueryRow(ctx,
		`SELECT id::text, status::text FROM payments WHERE id = $1::text::uuid`, id,
	).Scan(&p.PaymentID, &p.Status)

	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, ErrPaymentNotFound
	}
	if err != nil {
		return Payment{}, err
	}

	if s.cache != nil {
		s.cache.Set(ctx, p)
	}
	return p, nil
}
