# Payment Processing Service

A payment backend written in Go, backed by PostgreSQL. It moves money between accounts using a double-entry ledger and is designed so that retried or concurrent requests can never charge a customer twice.

> **Status: in progress.** Milestone 1 (core payment API) is complete. Later milestones are listed under [Roadmap](#roadmap) and are not built yet.

## Features (implemented)

- **Idempotent payment creation** — every request carries an `Idempotency-Key`. A retry with the same key and body returns the original result instead of creating a second payment. Reusing a key with a *different* body is rejected.
- **Transactional money movement** — each payment runs as a single SQL transaction: claim the key, lock both accounts, check funds, write the payment, write the ledger entries, update balances, commit. Any failure rolls the whole thing back.
- **Double-entry ledger** — every successful payment writes one debit and one credit row. The ledger is append-only.
- **Deadlock avoidance** — the two accounts in a payment are always locked in a fixed order (lowest id first), so opposite-direction payments between the same accounts cannot deadlock.
- **Money as integer cents** — amounts are stored as `BIGINT` minor units, never floats.
- **Failed payments are recorded** — an insufficient-funds attempt is stored as a `FAILED` payment (HTTP 402) and moves no money.
- **Concurrency test** — 100 goroutines fire the same request at the same instant and the test asserts exactly one payment is created.
- **Health check** — `GET /healthz` reports whether the database is reachable.

## API

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/payments` | Create a payment (idempotent) |
| `GET` | `/v1/payments/{id}` | Fetch a payment by id |
| `GET` | `/healthz` | Liveness and database check |

**Create a payment** — required headers: `X-Client-Id`, `Idempotency-Key`.

```json
{
  "payerAccountId": 1,
  "payeeAccountId": 2,
  "amountMinor": 1000,
  "currency": "USD"
}
```

| Status | Meaning |
|---|---|
| `201` | Payment created |
| `200` | Replay of an earlier request (response header `Idempotent-Replayed: true`) |
| `400` | Malformed request or invalid id |
| `402` | Payment recorded as `FAILED` (insufficient funds) |
| `404` | Account or payment not found |
| `422` | Idempotency key reused with a different body, or currency mismatch |
| `500` | Internal error (details are logged, not returned) |

## Architecture

    HTTP request
        │
        ▼
    internal/api        parse and validate the request, map errors to status codes
        │
        ▼
    internal/payments   business logic and transaction (idempotency, locking, ledger)
        │
        ▼
    PostgreSQL          source of truth; the idempotency gate is INSERT ... ON CONFLICT DO NOTHING

Correctness is enforced by the database, not by application code. The primary key on `(client_id, idempotency_key)` is what guarantees only one concurrent request wins, and `SELECT ... FOR UPDATE` serializes access to account balances.

## Tech Stack

- Go 1.27 (standard library `net/http`)
- PostgreSQL 17 via `pgx` v5 (`pgxpool`)
- Redis 7 (provisioned, not used yet — see roadmap)
- Docker Compose for local infrastructure

## Repository Structure

    .
    ├── cmd/server/          # Server entry point and /healthz
    ├── internal/
    │   ├── api/             # HTTP handlers
    │   └── payments/        # Payment logic and tests
    ├── db/
    │   ├── schema.sql       # Tables, enum, indexes (auto-applied on first container start)
    │   └── seed.sql         # Two demo accounts
    ├── loadtest/            # Sample request bodies
    ├── docker-compose.yml   # Postgres and Redis
    └── go.mod

## Running Locally

Requires Go, Docker Desktop, and Git. Commands below are for PowerShell.

**1. Start Postgres and Redis**

```
docker compose up -d
```

**2. Load the demo accounts** (`payer-1` with a large balance, `merchant-1` with zero)

```
Get-Content db/seed.sql | docker compose exec -T postgres psql -U payments -d payments
```

**3. Run the server**

```
go run ./cmd/server
```

The server listens on `:8080`. Set `DATABASE_URL` to override the default connection string.

**4. Create a payment**

```
curl.exe -i -X POST http://localhost:8080/v1/payments -H "X-Client-Id: demo" -H "Idempotency-Key: demo-1" -H "Content-Type: application/json" --data "@loadtest/pay-1000.json"
```

Run it twice: the first call returns `201`, the second returns `200` with `Idempotent-Replayed: true` and no second payment.

> The Postgres and Redis credentials in `docker-compose.yml` are throwaway **local development** values.

## Testing

```
go test ./...
```

Tests run against the local Postgres container, so start it first. Covered so far:

- 100 concurrent identical requests create exactly one payment
- Same idempotency key with a different body is rejected
- Insufficient funds produces a `FAILED` payment and moves no money

> The tests truncate the payment tables and reset the seed account balances, so run them against the local dev database only.

## Roadmap

### Milestone 1 — Core payment API ✅
- [x] Project skeleton, Docker Compose, schema, seed data
- [x] Idempotent, transactional `CreatePayment` with double-entry ledger
- [x] Concurrency, key-reuse, and insufficient-funds tests
- [x] `POST /v1/payments` and `GET /v1/payments/{id}`

### Milestone 2 — gRPC and ledger integrity
- [ ] gRPC interface alongside REST
- [ ] Ledger invariant checks (debits equal credits, balances match ledger history)
- [ ] Broader test coverage

### Milestone 3 — Performance
- [ ] Redis-based rate limiter and read cache
- [ ] Load testing with k6, with results saved
- [ ] Find and fix contention on hot accounts, with before/after measurements

### Milestone 4 — Reliability
- [ ] Reconciliation job that detects and reports ledger/payment mismatches
- [ ] Fault injection (crashes and failures mid-transaction)
- [ ] Metrics and dashboards with Prometheus and Grafana

### Milestone 5 — Polish
- [ ] Architecture and design-decision write-up
- [ ] Final README with measured results
