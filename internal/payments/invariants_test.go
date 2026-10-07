package payments

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// invSetup connects to the dev database, wipes payment data, and gives the
// payer 5000 cents and the merchant 0. It returns the pool, a service, and
// the payer and merchant account ids.
func invSetup(t *testing.T) (*pgxpool.Pool, *Service, int64, int64) {
	t.Helper()
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://payments:payments@localhost:5432/payments"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `TRUNCATE ledger_entries, idempotency_keys, payments`); err != nil {
		t.Fatal(err)
	}

	var payer, merchant int64
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE external_ref = 'payer-1'`).Scan(&payer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE external_ref = 'merchant-1'`).Scan(&merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET balance_minor = 5000 WHERE id = $1`, payer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET balance_minor = 0 WHERE id = $1`, merchant); err != nil {
		t.Fatal(err)
	}
	return pool, NewService(pool), payer, merchant
}

func invPay(t *testing.T, svc *Service, payer, merchant int64, key string, amount int64) {
	t.Helper()
	_, err := svc.CreatePayment(context.Background(), CreateRequest{
		ClientID:       "inv-test",
		IdempotencyKey: key,
		PayerAccountID: payer,
		PayeeAccountID: merchant,
		AmountMinor:    amount,
		Currency:       "USD",
	})
	if err != nil {
		t.Fatalf("payment %s: %v", key, err)
	}
}

func TestInvariantsHoldAfterNormalTraffic(t *testing.T) {
	_, svc, payer, merchant := invSetup(t)

	invPay(t, svc, payer, merchant, "k1", 1000)
	invPay(t, svc, payer, merchant, "k2", 2000)
	invPay(t, svc, payer, merchant, "k3", 500)
	invPay(t, svc, payer, merchant, "k4", 999999) // insufficient funds -> FAILED, no money moves

	violations, err := svc.CheckInvariants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected no violations, got %+v", violations)
	}
}

func TestInvariantsDetectMissingLedgerRow(t *testing.T) {
	pool, svc, payer, merchant := invSetup(t)

	invPay(t, svc, payer, merchant, "k1", 1000)
	invPay(t, svc, payer, merchant, "k2", 2000)

	// Deliberately corrupt the ledger: delete one entry.
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM ledger_entries WHERE id = (SELECT MIN(id) FROM ledger_entries)`); err != nil {
		t.Fatal(err)
	}

	violations, err := svc.CheckInvariants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 {
		t.Fatal("expected the checker to detect the deleted ledger row, but it found nothing")
	}
	t.Logf("detected as expected: %+v", violations)
}
