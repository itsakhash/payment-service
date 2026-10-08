CREATE TABLE IF NOT EXISTS reconciliation_baselines (
    account_id            BIGINT PRIMARY KEY REFERENCES accounts(id),
    implied_opening_minor BIGINT NOT NULL,
    recorded_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);