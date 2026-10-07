package paymentcache

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/itsakhash/payment-service/internal/payments"
)

// Redis caches payments in Redis with a fixed expiry.
type Redis struct {
	rdb *redis.Client
	ttl time.Duration
}

func New(rdb *redis.Client, ttl time.Duration) *Redis {
	return &Redis{rdb: rdb, ttl: ttl}
}

func key(id string) string { return "payment:" + strings.ToLower(id) }

func (c *Redis) Get(ctx context.Context, id string) (payments.Payment, bool) {
	raw, err := c.rdb.Get(ctx, key(id)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Printf("payment cache get failed (treating as miss): %v", err)
		}
		return payments.Payment{}, false
	}
	var p payments.Payment
	if err := json.Unmarshal(raw, &p); err != nil {
		return payments.Payment{}, false
	}
	return p, true
}

func (c *Redis) Set(ctx context.Context, p payments.Payment) {
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := c.rdb.Set(ctx, key(p.PaymentID), raw, c.ttl).Err(); err != nil {
		log.Printf("payment cache set failed: %v", err)
	}
}
