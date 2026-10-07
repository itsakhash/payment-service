package payments

import "context"

// Cache stores payments for fast reads. Implementations must treat any
// failure as a miss; the database is always the source of truth.
type Cache interface {
	Get(ctx context.Context, id string) (Payment, bool)
	Set(ctx context.Context, p Payment)
}

// WithCache enables read caching of payments. Call once at startup.
func (s *Service) WithCache(c Cache) *Service {
	s.cache = c
	return s
}
