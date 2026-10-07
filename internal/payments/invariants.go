package payments

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Violation describes one broken ledger invariant.
type Violation struct {
	Check  string
	Detail string
}

// CheckInvariants verifies the ledger is internally consistent.
// It runs in one read-only snapshot so concurrent writes can't cause false alarms.
func (s *Service) CheckInvariants(ctx context.Context) ([]Violation, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var out []Violation

	collect := func(check, query string) error {
		rows, err := tx.Query(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, Violation{Check: check, Detail: "payment " + id})
		}
		return rows.Err()
	}

	// 1. Every SUCCEEDED payment has exactly one debit on the payer and one
	//    credit on the payee, both equal to the payment amount, and nothing else.
	if err := collect("succeeded_payment_ledger_mismatch", `
		SELECT p.id::text
		FROM payments p
		LEFT JOIN ledger_entries l ON l.payment_id = p.id
		WHERE p.status = 'SUCCEEDED'
		GROUP BY p.id, p.amount_minor, p.payer_account_id, p.payee_account_id
		HAVING COUNT(l.id) <> 2
		    OR COUNT(*) FILTER (WHERE l.direction = 'D'
		           AND l.amount_minor = p.amount_minor
		           AND l.account_id = p.payer_account_id) <> 1
		    OR COUNT(*) FILTER (WHERE l.direction = 'C'
		           AND l.amount_minor = p.amount_minor
		           AND l.account_id = p.payee_account_id) <> 1`); err != nil {
		return nil, err
	}

	// 2. Payments that did not succeed must not have moved any money.
	if err := collect("unsuccessful_payment_has_ledger_rows", `
		SELECT DISTINCT p.id::text
		FROM payments p
		JOIN ledger_entries l ON l.payment_id = p.id
		WHERE p.status <> 'SUCCEEDED'`); err != nil {
		return nil, err
	}

	// 3. Across the whole ledger, total debits equal total credits.
	var net int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction = 'D' THEN amount_minor
		                         ELSE -amount_minor END), 0)::bigint
		FROM ledger_entries`).Scan(&net); err != nil {
		return nil, err
	}
	if net != 0 {
		out = append(out, Violation{
			Check:  "ledger_unbalanced",
			Detail: fmt.Sprintf("debits minus credits = %d", net),
		})
	}

	return out, nil
}
