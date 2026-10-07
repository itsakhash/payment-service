package payments

// recordSucceededSQL inserts the payment, both ledger rows, both balance
// changes, and the idempotency-key link in a single statement (one round trip).
// Parameters: $1 client id, $2 payer, $3 payee, $4 amount, $5 currency, $6 idempotency key.
const recordSucceededSQL = `
WITH p AS (
    INSERT INTO payments
        (id, client_id, payer_account_id, payee_account_id, amount_minor, currency, status, failure_reason)
    VALUES
        (gen_random_uuid(), $1, $2, $3, $4, $5, 'SUCCEEDED', NULL)
    RETURNING id
), l AS (
    INSERT INTO ledger_entries (payment_id, account_id, direction, amount_minor)
    SELECT id, $2::bigint, 'D', $4::bigint FROM p
    UNION ALL
    SELECT id, $3::bigint, 'C', $4::bigint FROM p
), d AS (
    UPDATE accounts SET balance_minor = balance_minor - $4 WHERE id = $2
), c AS (
    UPDATE accounts SET balance_minor = balance_minor + $4 WHERE id = $3
), k AS (
    UPDATE idempotency_keys SET payment_id = (SELECT id FROM p)
    WHERE client_id = $1 AND idem_key = $6
)
SELECT id::text FROM p`

// recordFailedSQL records an insufficient-funds payment (no money moves) and
// links the idempotency key. Same parameters as above.
const recordFailedSQL = `
WITH p AS (
    INSERT INTO payments
        (id, client_id, payer_account_id, payee_account_id, amount_minor, currency, status, failure_reason)
    VALUES
        (gen_random_uuid(), $1, $2, $3, $4, $5, 'FAILED', 'insufficient_funds')
    RETURNING id
), k AS (
    UPDATE idempotency_keys SET payment_id = (SELECT id FROM p)
    WHERE client_id = $1 AND idem_key = $6
)
SELECT id::text FROM p`
