package payments

import (
	"context"
	"testing"
)

func hasCheck(vs []Violation, check string) bool {
	for _, v := range vs {
		if v.Check == check {
			return true
		}
	}
	return false
}

func TestReconcileCleanThenDetectsDrift(t *testing.T) {
	ctx := context.Background()
	pool, svc, payer, merchant := invSetup(t)
	if _, err := pool.Exec(ctx, `TRUNCATE reconciliation_baselines`); err != nil {
		t.Fatal(err)
	}

	invPay(t, svc, payer, merchant, "r1", 1000)
	invPay(t, svc, payer, merchant, "r2", 500)

	vs, err := svc.Reconcile(ctx)
	if err != nil || len(vs) != 0 {
		t.Fatalf("first reconcile should be clean: %+v, err=%v", vs, err)
	}

	// More legitimate traffic must not look like drift.
	invPay(t, svc, payer, merchant, "r3", 250)
	vs, err = svc.Reconcile(ctx)
	if err != nil || len(vs) != 0 {
		t.Fatalf("reconcile after normal payments should be clean: %+v, err=%v", vs, err)
	}

	// Tamper with a balance without a ledger entry.
	if _, err := pool.Exec(ctx, `UPDATE accounts SET balance_minor = balance_minor + 777 WHERE id = $1`, payer); err != nil {
		t.Fatal(err)
	}
	vs, err = svc.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCheck(vs, "balance_drift") {
		t.Fatalf("expected balance_drift, got %+v", vs)
	}
	t.Logf("detected as expected: %+v", vs)
}

func TestReconcileFlagsKeyWithoutPayment(t *testing.T) {
	ctx := context.Background()
	pool, svc, _, _ := invSetup(t)
	if _, err := pool.Exec(ctx, `TRUNCATE reconciliation_baselines`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO idempotency_keys (client_id, idem_key, request_hash) VALUES ('orphan', 'k1', 'h')`); err != nil {
		t.Fatal(err)
	}
	vs, err := svc.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCheck(vs, "idempotency_key_without_payment") {
		t.Fatalf("expected idempotency_key_without_payment, got %+v", vs)
	}
}
