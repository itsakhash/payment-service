package payments

import (
	"context"
	"testing"
	"time"
)

func TestAccountLimiterBlocksIsolatesAndCleansUp(t *testing.T) {
	l := newAccountLimiter(1)
	ctx := context.Background()

	rel1, err := l.acquire(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}

	// A different account is not blocked by account 7.
	other, err := l.acquire(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	other()

	// A second request on account 7 must wait; with a short deadline it gives up.
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := l.acquire(short, 7); err == nil {
		t.Fatal("second acquire on the same account should block until the deadline")
	}

	// After release, the account is available again.
	rel1()
	rel2, err := l.acquire(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	rel2()

	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("limiter leaked %d entries", n)
	}
}
