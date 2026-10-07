package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestLimiter(t *testing.T, capacity int, rate float64) *Limiter {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	t.Cleanup(func() { rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis not reachable: %v", err)
	}
	return New(rdb, capacity, rate)
}

func TestTokenBucketLimitsAndRefills(t *testing.T) {
	l := newTestLimiter(t, 3, 2) // burst of 3, refills 2 tokens per second
	ctx := context.Background()
	key := fmt.Sprintf("test-%d", time.Now().UnixNano())

	for i := 1; i <= 3; i++ {
		ok, _, err := l.Allow(ctx, key)
		if err != nil || !ok {
			t.Fatalf("request %d should be allowed (ok=%v err=%v)", i, ok, err)
		}
	}

	ok, retry, err := l.Allow(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("4th request should be limited")
	}
	if retry <= 0 || retry > time.Second {
		t.Fatalf("retry-after out of range: %v", retry)
	}

	// A different client has its own bucket.
	if ok, _, _ := l.Allow(ctx, key+"-other"); !ok {
		t.Fatal("a different client should not be limited")
	}

	// After enough time for a token to refill, the first client is allowed again.
	time.Sleep(600 * time.Millisecond)
	if ok, _, _ := l.Allow(ctx, key); !ok {
		t.Fatal("request should be allowed after a token refills")
	}
}

func TestMiddlewareReturns429(t *testing.T) {
	l := newTestLimiter(t, 1, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	h := Middleware(l, next)
	client := fmt.Sprintf("mw-%d", time.Now().UnixNano())

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-Client-Id", client)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if got := do("POST", "/v1/payments").Code; got != http.StatusCreated {
		t.Fatalf("first request: got %d, want 201", got)
	}
	rec := do("POST", "/v1/payments")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 response is missing Retry-After")
	}

	// Reads and other paths are never limited.
	if got := do("GET", "/healthz").Code; got != http.StatusCreated {
		t.Fatalf("GET /healthz should pass through, got %d", got)
	}
}
