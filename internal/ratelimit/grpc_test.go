package ratelimit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryInterceptorLimitsPerClient(t *testing.T) {
	ctx := context.Background()
	opt, err := redis.ParseURL("redis://localhost:6379/0")
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}

	const method = "/payment.v1.PaymentService/CreatePayment"
	l := New(rdb, 3, 0.5) // burst of 3, then 1 token every 2 seconds
	ic := UnaryInterceptor(l, method, func(req any) string { return req.(string) })
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: method}

	client := fmt.Sprintf("grpc-test-%d", time.Now().UnixNano())

	// The burst is allowed.
	for i := 0; i < 3; i++ {
		if _, err := ic(ctx, client, info, handler); err != nil {
			t.Fatalf("call %d should be allowed: %v", i+1, err)
		}
	}
	// The next call is rejected with ResourceExhausted.
	if _, err := ic(ctx, client, info, handler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("4th call: want ResourceExhausted, got %v", err)
	}
	// A different client has its own bucket.
	if _, err := ic(ctx, client+"-other", info, handler); err != nil {
		t.Fatalf("other client should be unaffected: %v", err)
	}
	// Other methods are never limited.
	otherMethod := &grpc.UnaryServerInfo{FullMethod: "/payment.v1.PaymentService/GetPayment"}
	if _, err := ic(ctx, client, otherMethod, handler); err != nil {
		t.Fatalf("other method should not be limited: %v", err)
	}
	// An empty client id passes through to the handler.
	if _, err := ic(ctx, "", info, handler); err != nil {
		t.Fatalf("empty client id should pass through: %v", err)
	}
}
