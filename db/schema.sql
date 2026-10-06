CREATE TABLE accounts (
  id            BIGSERIAL PRIMARY KEY,
  external_ref  TEXT NOT NULL UNIQUE,
  currency      CHAR(3) NOT NULL,
  balance_minor BIGINT NOT NULL DEFAULT 0 CHECK (balance_minor >= 0)
);

CREATE TYPE payment_status AS ENUM ('PENDING', 'SUCCEEDED', 'FAILED');

CREATE TABLE payments (
  id               UUID PRIMARY KEY,
  client_id        TEXT NOT NULL,
  payer_account_id BIGINT NOT NULL REFERENCES accounts(id),
  payee_account_id BIGINT NOT NULL REFERENCES accounts(id),
  amount_minor     BIGINT NOT NULL CHECK (amount_minor > 0),
  currency         CHAR(3) NOT NULL,
  status           payment_status NOT NULL,
  failure_reason   TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE idempotency_keys (
  client_id     TEXT NOT NULL,
  idem_key      TEXT NOT NULL,
  request_hash  TEXT NOT NULL,
  payment_id    UUID REFERENCES payments(id),
  response_code INT,
  response_body JSONB,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (client_id, idem_key)
);

-- Append-only double-entry ledger: never UPDATE or DELETE rows here.
CREATE TABLE ledger_entries (
  id           BIGSERIAL PRIMARY KEY,
  payment_id   UUID NOT NULL REFERENCES payments(id),
  account_id   BIGINT NOT NULL REFERENCES accounts(id),
  direction    CHAR(1) NOT NULL CHECK (direction IN ('D', 'C')),
  amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON ledger_entries (payment_id);
CREATE INDEX ON ledger_entries (account_id);