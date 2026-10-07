package payments

import (
	"context"
	"errors"
	"testing"
)

type fakeCache struct {
	m                map[string]Payment
	gets, hits, sets int
}

func (f *fakeCache) Get(ctx context.Context, id string) (Payment, bool) {
	f.gets++
	p, ok := f.m[id]
	if ok {
		f.hits++
	}
	return p, ok
}

func (f *fakeCache) Set(ctx context.Context, p Payment) {
	f.sets++
	f.m[p.PaymentID] = p
}

func TestGetPaymentUsesCache(t *testing.T) {
	ctx := context.Background()
	_, svc, payer, merchant := invSetup(t)
	fc := &fakeCache{m: map[string]Payment{}}
	svc.WithCache(fc)

	res, err := svc.CreatePayment(ctx, CreateRequest{
		ClientID:       "cache-test",
		IdempotencyKey: "c1",
		PayerAccountID: payer,
		PayeeAccountID: merchant,
		AmountMinor:    100,
		Currency:       "USD",
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := svc.GetPayment(ctx, res.PaymentID) // miss: reads the database, fills the cache
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.GetPayment(ctx, res.PaymentID) // hit
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("cached answer differs: %+v vs %+v", first, second)
	}
	if fc.hits != 1 || fc.sets != 1 {
		t.Fatalf("want 1 hit and 1 set, got hits=%d sets=%d", fc.hits, fc.sets)
	}

	// An unknown id is not cached.
	if _, err := svc.GetPayment(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrPaymentNotFound) {
		t.Fatalf("want ErrPaymentNotFound, got %v", err)
	}
	if fc.sets != 1 {
		t.Fatalf("a not-found result must not be cached, sets=%d", fc.sets)
	}

	// A malformed id never touches the cache.
	gets := fc.gets
	if _, err := svc.GetPayment(ctx, "nope"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
	if fc.gets != gets {
		t.Fatal("a malformed id should be rejected before the cache is consulted")
	}
}
