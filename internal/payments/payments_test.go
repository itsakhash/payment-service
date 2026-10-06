package payments

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://payments:payments@localhost:5432/payments"
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// queryInt runs a query that returns one integer.
func queryInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

// resetState wipes all payment data and puts the two seed accounts back to known balances.
// This is the local development database only.
func resetState(t *testing.T, pool *pgxpool.Pool) (payerID, merchantID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE ledger_entries, idempotency_keys, payments"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE accounts SET balance_minor = 100000000 WHERE external_ref = 'payer-1'"); err != nil {
		t.Fatalf("reset payer: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE accounts SET balance_minor = 0 WHERE external_ref = 'merchant-1'"); err != nil {
		t.Fatalf("reset merchant: %v", err)
	}
	payerID = queryInt(t, pool, "SELECT id FROM accounts WHERE external_ref = 'payer-1'")
	merchantID = queryInt(t, pool, "SELECT id FROM accounts WHERE external_ref = 'merchant-1'")
	return payerID, merchantID
}

func TestConcurrentSameKeyCreatesExactlyOnePayment(t *testing.T) {
	pool := testPool(t)
	payerID, merchantID := resetState(t, pool)
	svc := NewService(pool)

	const workers = 100
	const amount = int64(1000) // $10.00, in cents

	req := CreateRequest{
		ClientID:       "client-1",
		IdempotencyKey: "same-key-for-everyone",
		PayerAccountID: payerID,
		PayeeAccountID: merchantID,
		AmountMinor:    amount,
		Currency:       "USD",
	}

	results := make([]Result, workers)
	errs := make([]error, workers)

	// All goroutines wait on 'start', then we release them at the same moment.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.CreatePayment(context.Background(), req)
		}(i)
	}
	close(start)
	wg.Wait()

	// 1. No request should fail.
	failed := 0
	var firstErr error
	for _, err := range errs {
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if failed > 0 {
		t.Fatalf("%d of %d requests failed; first error: %v", failed, workers, firstErr)
	}

	// 2. Every caller must get the same payment id back.
	first := results[0].PaymentID
	if first == "" {
		t.Fatalf("payment id is empty")
	}
	created := 0
	for i, r := range results {
		if r.PaymentID != first {
			t.Errorf("request %d got payment %q, want %q", i, r.PaymentID, first)
		}
		if !r.Replayed {
			created++
		}
	}

	// 3. Exactly one request actually created the payment; the other 99 were replays.
	if created != 1 {
		t.Errorf("%d requests created a payment, want exactly 1", created)
	}

	// 4. The database must agree: one payment, one debit and one credit.
	if n := queryInt(t, pool, "SELECT count(*) FROM payments"); n != 1 {
		t.Errorf("payments rows = %d, want 1", n)
	}
	if n := queryInt(t, pool, "SELECT count(*) FROM ledger_entries"); n != 2 {
		t.Errorf("ledger_entries rows = %d, want 2", n)
	}

	// 5. Money moved exactly once.
	payerBalance := queryInt(t, pool, "SELECT balance_minor FROM accounts WHERE id = $1", payerID)
	if payerBalance != 100000000-amount {
		t.Errorf("payer balance = %d, want %d", payerBalance, 100000000-amount)
	}
	merchantBalance := queryInt(t, pool, "SELECT balance_minor FROM accounts WHERE id = $1", merchantID)
	if merchantBalance != amount {
		t.Errorf("merchant balance = %d, want %d", merchantBalance, amount)
	}
}

func TestSameKeyDifferentBodyIsRejected(t *testing.T) {
	pool := testPool(t)
	payerID, merchantID := resetState(t, pool)
	svc := NewService(pool)

	req := CreateRequest{
		ClientID:       "client-1",
		IdempotencyKey: "key-abc",
		PayerAccountID: payerID,
		PayeeAccountID: merchantID,
		AmountMinor:    1000,
		Currency:       "USD",
	}
	if _, err := svc.CreatePayment(context.Background(), req); err != nil {
		t.Fatalf("first request: %v", err)
	}

	// Same key, different body: this must be rejected, not treated as a retry.
	req.AmountMinor = 2000
	if _, err := svc.CreatePayment(context.Background(), req); err != ErrKeyReused {
		t.Errorf("got error %v, want ErrKeyReused", err)
	}

	if n := queryInt(t, pool, "SELECT count(*) FROM payments"); n != 1 {
		t.Errorf("payments rows = %d, want 1", n)
	}
	payerBalance := queryInt(t, pool, "SELECT balance_minor FROM accounts WHERE id = $1", payerID)
	if payerBalance != 100000000-1000 {
		t.Errorf("payer balance = %d, want %d", payerBalance, 100000000-1000)
	}
}

func TestInsufficientFundsFailsWithoutMovingMoney(t *testing.T) {
	pool := testPool(t)
	payerID, merchantID := resetState(t, pool)
	if _, err := pool.Exec(context.Background(),
		"UPDATE accounts SET balance_minor = 500 WHERE id = $1", payerID); err != nil {
		t.Fatalf("set balance: %v", err)
	}
	svc := NewService(pool)

	req := CreateRequest{
		ClientID:       "client-1",
		IdempotencyKey: "key-poor",
		PayerAccountID: payerID,
		PayeeAccountID: merchantID,
		AmountMinor:    1000, // more than the 500 available
		Currency:       "USD",
	}

	first, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if first.Status != "FAILED" {
		t.Errorf("status = %q, want FAILED", first.Status)
	}
	if first.Replayed {
		t.Errorf("first request should not be a replay")
	}

	// A retry with the same key must replay the same FAILED answer.
	second, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !second.Replayed || second.PaymentID != first.PaymentID || second.Status != "FAILED" {
		t.Errorf("retry = %+v, want a replay of %+v", second, first)
	}

	if n := queryInt(t, pool, "SELECT count(*) FROM payments"); n != 1 {
		t.Errorf("payments rows = %d, want 1", n)
	}
	if n := queryInt(t, pool, "SELECT count(*) FROM ledger_entries"); n != 0 {
		t.Errorf("ledger_entries rows = %d, want 0 (no money should move)", n)
	}
	if b := queryInt(t, pool, "SELECT balance_minor FROM accounts WHERE id = $1", payerID); b != 500 {
		t.Errorf("payer balance = %d, want 500", b)
	}
	if b := queryInt(t, pool, "SELECT balance_minor FROM accounts WHERE id = $1", merchantID); b != 0 {
		t.Errorf("merchant balance = %d, want 0", b)
	}
}
