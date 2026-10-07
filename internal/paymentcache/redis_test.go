package paymentcache

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/itsakhash/payment-service/internal/payments"
)

func TestRedisCacheRoundTripAndExpiry(t *testing.T) {
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
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis not reachable: %v", err)
	}

	c := New(rdb, 300*time.Millisecond)
	id := fmt.Sprintf("00000000-0000-0000-0000-%012d", time.Now().UnixNano()%1_000_000_000_000)

	if _, ok := c.Get(ctx, id); ok {
		t.Fatal("unexpected hit on an empty cache")
	}

	want := payments.Payment{PaymentID: id, Status: "SUCCEEDED"}
	c.Set(ctx, want)
	got, ok := c.Get(ctx, id)
	if !ok || got != want {
		t.Fatalf("round trip failed: got %+v ok=%v, want %+v", got, ok, want)
	}

	time.Sleep(450 * time.Millisecond)
	if _, ok := c.Get(ctx, id); ok {
		t.Fatal("entry should have expired")
	}
}
