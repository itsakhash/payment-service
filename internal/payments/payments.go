package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateRequest is everything needed to create one payment.
// Money is always whole cents (never floating point).
type CreateRequest struct {
	ClientID       string
	IdempotencyKey string
	PayerAccountID int64
	PayeeAccountID int64
	AmountMinor    int64
	Currency       string
}

// Result is what a caller gets back from CreatePayment.
type Result struct {
	PaymentID string
	Status    string // "SUCCEEDED" or "FAILED"
	Replayed  bool   // true if this answer was replayed from an earlier identical request
}

var (
	ErrInvalidRequest  = errors.New("invalid request")
	ErrAccountNotFound = errors.New("account not found")
	ErrKeyReused       = errors.New("idempotency key reused with a different request")
)

// Service holds the business logic. Both the REST and gRPC APIs will call it.
type Service struct {
	pool    *pgxpool.Pool
	limiter *accountLimiter
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, limiter: newAccountLimiter(accountConcurrencyFromEnv())}
}

// requestHash fingerprints the contents of a request, so we can tell a true
// retry (same key, same contents) from a misuse (same key, different contents).
func requestHash(req CreateRequest) string {
	text := fmt.Sprintf("%d|%d|%d|%s", req.PayerAccountID, req.PayeeAccountID, req.AmountMinor, req.Currency)
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// CreatePayment moves money from payer to payee exactly once per idempotency key.
// Everything happens inside ONE database transaction: either all of it commits,
// or none of it does.
func (s *Service) CreatePayment(ctx context.Context, req CreateRequest) (Result, error) {
	if req.ClientID == "" || req.IdempotencyKey == "" || req.Currency == "" ||
		req.AmountMinor <= 0 || req.PayerAccountID == req.PayeeAccountID {
		return Result{}, ErrInvalidRequest
	}

	hash := requestHash(req)

	// Wait for a slot on both accounts BEFORE taking a database connection,
	// so requests queued behind a hot account don't tie up the pool.
	release, err := s.limiter.acquirePair(ctx, req.PayerAccountID, req.PayeeAccountID)
	if err != nil {
		return Result{}, fmt.Errorf("wait for account slot: %w", err)
	}
	defer release()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin: %w", err)
	}
	// If we return before Commit, this undoes everything. After a successful
	// Commit it does nothing.
	defer tx.Rollback(ctx)

	// PART 1: claim the idempotency key.
	// If another request holds the same key and has not finished yet, Postgres
	// makes this INSERT wait until that request commits or rolls back.
	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (client_id, idem_key, request_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`,
		req.ClientID, req.IdempotencyKey, hash)
	if err != nil {
		return Result{}, fmt.Errorf("claim key: %w", err)
	}

	if tag.RowsAffected() == 0 {
		// The key already exists: this is a retry. Replay the stored answer.
		var storedHash, storedPaymentID string
		err := tx.QueryRow(ctx, `
			SELECT request_hash, COALESCE(payment_id::text, '')
			FROM idempotency_keys
			WHERE client_id = $1 AND idem_key = $2`,
			req.ClientID, req.IdempotencyKey).Scan(&storedHash, &storedPaymentID)
		if err != nil {
			return Result{}, fmt.Errorf("read stored key: %w", err)
		}
		if storedHash != hash {
			return Result{}, ErrKeyReused
		}
		if storedPaymentID == "" {
			return Result{}, errors.New("stored key has no payment")
		}
		var status string
		err = tx.QueryRow(ctx,
			`SELECT status::text FROM payments WHERE id = $1::text::uuid`,
			storedPaymentID).Scan(&status)
		if err != nil {
			return Result{}, fmt.Errorf("read stored payment: %w", err)
		}
		return Result{PaymentID: storedPaymentID, Status: status, Replayed: true}, nil
	}

	// PART 2: lock both accounts, always lowest id first.
	// A consistent order means two payments going in opposite directions
	// can never wait on each other forever (a deadlock).
	rows, err := tx.Query(ctx, `
		SELECT id, balance_minor, currency
		FROM accounts
		WHERE id IN ($1, $2)
		ORDER BY id
		FOR UPDATE`,
		req.PayerAccountID, req.PayeeAccountID)
	if err != nil {
		return Result{}, fmt.Errorf("lock accounts: %w", err)
	}
	balances := map[int64]int64{}
	currencies := map[int64]string{}
	for rows.Next() {
		var id, balance int64
		var currency string
		if err := rows.Scan(&id, &balance, &currency); err != nil {
			rows.Close()
			return Result{}, fmt.Errorf("scan account: %w", err)
		}
		balances[id] = balance
		currencies[id] = currency
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("read accounts: %w", err)
	}
	if len(balances) != 2 {
		return Result{}, ErrAccountNotFound
	}
	if currencies[req.PayerAccountID] != req.Currency || currencies[req.PayeeAccountID] != req.Currency {
		return Result{}, fmt.Errorf("%w: currency mismatch", ErrInvalidRequest)
	}

	// PART 3: decide the outcome.
	status := "SUCCEEDED"
	if balances[req.PayerAccountID] < req.AmountMinor {
		status = "FAILED"
	}

	// PART 4: record everything in ONE statement (one round trip) so the
	// account locks are held for as little time as possible.
	query := recordSucceededSQL
	if status == "FAILED" {
		query = recordFailedSQL
	}
	var paymentID string
	err = tx.QueryRow(ctx, query,
		req.ClientID, req.PayerAccountID, req.PayeeAccountID, req.AmountMinor,
		req.Currency, req.IdempotencyKey).Scan(&paymentID)
	if err != nil {
		return Result{}, fmt.Errorf("record payment: %w", err)
	}

	// PART 5: commit everything at once.
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit: %w", err)
	}

	return Result{PaymentID: paymentID, Status: status, Replayed: false}, nil
}
