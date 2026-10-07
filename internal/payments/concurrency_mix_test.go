package payments

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestConcurrentOppositeDirectionPayments(t *testing.T) {
	ctx := context.Background()
	pool, svc, a, b := invSetup(t)

	const opening = int64(1_000_000)
	for _, id := range []int64{a, b} {
		if _, err := pool.Exec(ctx, `UPDATE accounts SET balance_minor = $2 WHERE id = $1`, id, opening); err != nil {
			t.Fatal(err)
		}
	}

	type job struct {
		from, to int64
		key      string
		amount   int64
	}

	const unique = 200
	wantA, wantB := opening, opening
	var jobs []job
	for i := 0; i < unique; i++ {
		from, to := a, b
		if i%2 == 1 {
			from, to = b, a
		}
		amount := int64(1 + i%50)
		if from == a {
			wantA -= amount
			wantB += amount
		} else {
			wantB -= amount
			wantA += amount
		}
		jb := job{from: from, to: to, key: fmt.Sprintf("mix-%d", i), amount: amount}
		jobs = append(jobs, jb, jb) // every payment is sent twice, identically
	}

	startCh := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, len(jobs))

	for _, jb := range jobs {
		wg.Add(1)
		go func(jb job) {
			defer wg.Done()
			<-startCh
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := svc.CreatePayment(cctx, CreateRequest{
				ClientID:       "mix-test",
				IdempotencyKey: jb.key,
				PayerAccountID: jb.from,
				PayeeAccountID: jb.to,
				AmountMinor:    jb.amount,
				Currency:       "USD",
			})
			if err != nil {
				errs <- fmt.Errorf("%s: %w", jb.key, err)
			}
		}(jb)
	}

	close(startCh)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	var payments, entries int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM payments`).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ledger_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if payments != unique {
		t.Errorf("payments = %d, want %d (duplicates must not create extra payments)", payments, unique)
	}
	if entries != unique*2 {
		t.Errorf("ledger entries = %d, want %d", entries, unique*2)
	}

	var gotA, gotB int64
	if err := pool.QueryRow(ctx, `SELECT balance_minor FROM accounts WHERE id = $1`, a).Scan(&gotA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance_minor FROM accounts WHERE id = $1`, b).Scan(&gotB); err != nil {
		t.Fatal(err)
	}
	if gotA != wantA || gotB != wantB {
		t.Errorf("balances A=%d B=%d, want A=%d B=%d", gotA, gotB, wantA, wantB)
	}
	if gotA+gotB != 2*opening {
		t.Errorf("money was created or destroyed: total = %d, want %d", gotA+gotB, 2*opening)
	}

	violations, err := svc.CheckInvariants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Errorf("invariant violations: %+v", violations)
	}
}
