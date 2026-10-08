package payments

import (
	"context"
	"errors"
	"testing"
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
