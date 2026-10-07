package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenBucket refills continuously and spends one token per request.
// It runs atomically inside Redis and uses Redis's clock, so multiple
// server instances agree on time.
var tokenBucket = redis.NewScript(`
local key      = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate     = tonumber(ARGV[2])

local t   = redis.call('TIME')
local now = t[1] * 1000 + math.floor(t[2] / 1000)

local data   = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])
if tokens == nil then
  tokens = capacity
  ts = now
end

local elapsed = math.max(0, now - ts)
tokens = math.min(capacity, tokens + elapsed * rate / 1000)

local allowed = 0
local retry   = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry = math.ceil((1 - tokens) * 1000 / rate)
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
redis.call('PEXPIRE', key, math.ceil(capacity / rate * 1000) * 2)
return {allowed, retry}
`)

// Limiter is a per-key token-bucket rate limiter backed by Redis.
type Limiter struct {
	rdb        *redis.Client
	capacity   int
	ratePerSec float64
}

// New returns a limiter allowing bursts of up to capacity requests and a
// sustained rate of ratePerSec requests per second per key.
func New(rdb *redis.Client, capacity int, ratePerSec float64) *Limiter {
	return &Limiter{rdb: rdb, capacity: capacity, ratePerSec: ratePerSec}
}

// Allow spends one token for key. If the bucket is empty it returns false and
// how long to wait before retrying.
func (l *Limiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	res, err := tokenBucket.Run(ctx, l.rdb, []string{"rl:" + key}, l.capacity, l.ratePerSec).Slice()
	if err != nil {
		return false, 0, err
	}
	if len(res) != 2 {
		return false, 0, fmt.Errorf("unexpected limiter reply: %v", res)
	}
	allowed, _ := res[0].(int64)
	retryMs, _ := res[1].(int64)
	return allowed == 1, time.Duration(retryMs) * time.Millisecond, nil
}
