package payments

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ledgerNet is the net effect of an account's ledger entries (credits minus debits).
const ledgerNet = `COALESCE(SUM(CASE l.direction WHEN 'C' THEN l.amount_minor ELSE -l.amount_minor END), 0)`

// Reconcile runs every consistency check: the ledger invariants, balance drift
// against the ledger, and idempotency keys left without a payment.
func (s *Service) Reconcile(ctx context.Context) ([]Violation, error) {
	out, err := s.CheckInvariants(ctx)
	if err != nil {
		return nil, err
	}
	more, err := s.checkBalancesAndKeys(ctx)
	if err != nil {
		return nil, err
	}
	return append(out, more...), nil
}

func (s *Service) checkBalancesAndKeys(ctx context.Context) ([]Violation, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var out []Violation

	// Record a baseline the first time an account is seen.
	if _, err := tx.Exec(ctx, `
		INSERT INTO reconciliation_baselines (account_id, implied_opening_minor)
		SELECT a.id, (a.balance_minor - `+ledgerNet+`)::bigint
		FROM accounts a
		LEFT JOIN ledger_entries l ON l.account_id = a.id
		GROUP BY a.id, a.balance_minor
		ON CONFLICT (account_id) DO NOTHING`); err != nil {
		return nil, err
	}

	// Any account whose implied opening balance has moved has drifted.
	rows, err := tx.Query(ctx, `
		SELECT a.id, (a.balance_minor - `+ledgerNet+` - b.implied_opening_minor)::bigint
		FROM accounts a
		JOIN reconciliation_baselines b ON b.account_id = a.id
		LEFT JOIN ledger_entries l ON l.account_id = a.id
		GROUP BY a.id, a.balance_minor, b.implied_opening_minor
		HAVING a.balance_minor - `+ledgerNet+` <> b.implied_opening_minor
		ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, drift int64
		if err := rows.Scan(&id, &drift); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, Violation{
			Check:  "balance_drift",
			Detail: fmt.Sprintf("account %d balance differs from its ledger by %d", id, drift),
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// A committed idempotency key must always point at a payment.
	keyRows, err := tx.Query(ctx, `
		SELECT client_id || '/' || idem_key FROM idempotency_keys WHERE payment_id IS NULL`)
	if err != nil {
		return nil, err
	}
	for keyRows.Next() {
		var k string
		if err := keyRows.Scan(&k); err != nil {
			keyRows.Close()
			return nil, err
		}
		out = append(out, Violation{Check: "idempotency_key_without_payment", Detail: "key " + k})
	}
	keyRows.Close()
	if err := keyRows.Err(); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
