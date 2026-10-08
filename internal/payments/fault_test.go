package payments

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errInjected = errors.New("injected failure")

// failAt returns a fault hook that fails only at the given point.
func failAt(p faultPoint) func(faultPoint) error {
	return func(got faultPoint) error {
		if got == p {
			return errInjected
		}
		return nil
	}
}

// A crash BEFORE commit must leave no trace, and a retry must then succeed once.
func TestCrashBeforeCommitLeavesNoTrace(t *testing.T) {
	for _, point := range []faultPoint{faultAfterKeyClaim, faultAfterLock, faultBeforeCommit} {
		t.Run(string(point), func(t *testing.T) {
			ctx := context.Background()
			pool, svc, payerID, merchantID := invSetup(t)

			count := func(q string, args ...any) int64 {
				var n int64
				if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			balance := func(id int64) int64 {
				return count(`SELECT balance_minor FROM accounts WHERE id = $1`, id)
			}
			payerBefore, merchantBefore := balance(payerID), balance(merchantID)

			req := CreateRequest{
				ClientID: "fault", IdempotencyKey: "crash-" + string(point),
				PayerAccountID: payerID, PayeeAccountID: merchantID,
				AmountMinor: 1000, Currency: "USD",
			}

			// 1. Crash partway through.
			svc.fault = failAt(point)
			if _, err := svc.CreatePayment(ctx, req); !errors.Is(err, errInjected) {
				t.Fatalf("expected injected failure, got %v", err)
			}

			// 2. Nothing may have been left behind.
			if n := count(`SELECT count(*) FROM payments WHERE client_id = 'fault'`); n != 0 {
				t.Fatalf("payments left behind: %d", n)
			}
			if n := count(`SELECT count(*) FROM idempotency_keys WHERE client_id = 'fault'`); n != 0 {
				t.Fatalf("idempotency keys left behind: %d", n)
			}
			if n := count(`SELECT count(*) FROM ledger_entries`); n != 0 {
				t.Fatalf("ledger rows left behind: %d", n)
			}
			if balance(payerID) != payerBefore || balance(merchantID) != merchantBefore {
				t.Fatal("balances changed after a crash")
			}

			// 3. The client retries with the same key (no fault now): it must work, once.
			svc.fault = nil
			res, err := svc.CreatePayment(ctx, req)
			if err != nil || res.Status != "SUCCEEDED" || res.Replayed {
				t.Fatalf("retry after crash: %+v, %v", res, err)
			}
			if balance(payerID) != payerBefore-1000 || balance(merchantID) != merchantBefore+1000 {
				t.Fatal("retry did not move exactly 1000")
			}

			violations, err := svc.CheckInvariants(ctx)
			if err != nil || len(violations) != 0 {
				t.Fatalf("invariants: %v, %v", violations, err)
			}
		})
	}
}

// A crash AFTER commit (the client never sees the answer) must not cause a second
// charge when the client retries: the retry replays the stored result.
func TestCrashAfterCommitRetryDoesNotDoubleCharge(t *testing.T) {
	ctx := context.Background()
	pool, svc, payerID, merchantID := invSetup(t)

	count := func(q string, args ...any) int64 {
		var n int64
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	payerBefore := count(`SELECT balance_minor FROM accounts WHERE id = $1`, payerID)

	req := CreateRequest{
		ClientID: "fault", IdempotencyKey: "crash-after-commit",
		PayerAccountID: payerID, PayeeAccountID: merchantID,
		AmountMinor: 1000, Currency: "USD",
	}

	svc.fault = failAt(faultAfterCommit)
	if _, err := svc.CreatePayment(ctx, req); !errors.Is(err, errInjected) {
		t.Fatalf("expected injected failure, got %v", err)
	}
	if n := count(`SELECT count(*) FROM payments WHERE client_id = 'fault'`); n != 1 {
		t.Fatalf("payment should have committed, found %d", n)
	}

	svc.fault = nil
	res, err := svc.CreatePayment(ctx, req)
	if err != nil || !res.Replayed || res.Status != "SUCCEEDED" {
		t.Fatalf("retry: %+v, %v", res, err)
	}

	if n := count(`SELECT count(*) FROM payments WHERE client_id = 'fault'`); n != 1 {
		t.Fatalf("expected exactly 1 payment, found %d", n)
	}
	if got := count(`SELECT balance_minor FROM accounts WHERE id = $1`, payerID); got != payerBefore-1000 {
		t.Fatalf("payer charged wrong amount: before %d, after %d", payerBefore, got)
	}

	violations, err := svc.CheckInvariants(ctx)
	if err != nil || len(violations) != 0 {
		t.Fatalf("invariants: %v, %v", violations, err)
	}
}

// Chaos: many concurrent payments, random injected failures and killed database
// connections, clients retrying with the same key. Money must move exactly once each.
func TestChaosRetriesMoveMoneyExactlyOnce(t *testing.T) {
	ctx := context.Background()
	pool, svc, payerID, merchantID := invSetup(t)

	const n = 40
	const amount = 100

	count := func(q string, args ...any) int64 {
		var v int64
		if err := pool.QueryRow(ctx, q, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	payerBefore := count(`SELECT balance_minor FROM accounts WHERE id = $1`, payerID)
	merchantBefore := count(`SELECT balance_minor FROM accounts WHERE id = $1`, merchantID)

	var injected, killed int64
	svc.fault = func(p faultPoint) error {
		// Occasionally kill every other backend that is sitting inside a transaction.
		if p == faultAfterLock && rand.Intn(100) < 5 {
			_, _ = pool.Exec(ctx, `
				SELECT pg_terminate_backend(pid) FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()
				  AND state = 'idle in transaction'`)
			atomic.AddInt64(&killed, 1)
		}
		if rand.Intn(100) < 15 {
			atomic.AddInt64(&injected, 1)
			return errInjected
		}
		return nil
	}

	var wg sync.WaitGroup
	var attempts int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := CreateRequest{
				ClientID: "chaos", IdempotencyKey: fmt.Sprintf("chaos-%d", i),
				PayerAccountID: payerID, PayeeAccountID: merchantID,
				AmountMinor: amount, Currency: "USD",
			}
			for try := 0; try < 200; try++ {
				atomic.AddInt64(&attempts, 1)
				res, err := svc.CreatePayment(ctx, req)
				if err == nil {
					if res.Status != "SUCCEEDED" {
						t.Errorf("payment %d: status %s", i, res.Status)
					}
					return
				}
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
			t.Errorf("payment %d never succeeded", i)
		}(i)
	}
	wg.Wait()
	svc.fault = nil

	t.Logf("%d payments took %d attempts; %d injected errors, %d connection-kill rounds",
		n, attempts, injected, killed)

	if got := count(`SELECT count(*) FROM payments WHERE client_id = 'chaos'`); got != n {
		t.Fatalf("expected %d payments, found %d", n, got)
	}
	if got := count(`SELECT balance_minor FROM accounts WHERE id = $1`, payerID); got != payerBefore-n*amount {
		t.Fatalf("payer balance %d, want %d", got, payerBefore-n*amount)
	}
	if got := count(`SELECT balance_minor FROM accounts WHERE id = $1`, merchantID); got != merchantBefore+n*amount {
		t.Fatalf("merchant balance %d, want %d", got, merchantBefore+n*amount)
	}

	violations, err := svc.CheckInvariants(ctx)
	if err != nil || len(violations) != 0 {
		t.Fatalf("invariants: %v, %v", violations, err)
	}
}
