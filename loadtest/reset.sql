TRUNCATE ledger_entries, idempotency_keys, payments;
UPDATE accounts SET balance_minor = 100000000 WHERE external_ref = 'payer-1';
UPDATE accounts SET balance_minor = 0 WHERE external_ref = 'merchant-1';
UPDATE accounts SET balance_minor = 100000000 WHERE external_ref LIKE 'bench-payer-%';
UPDATE accounts SET balance_minor = 0 WHERE external_ref LIKE 'bench-merchant-%';