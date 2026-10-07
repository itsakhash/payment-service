# Payment Processing Service

A payment backend written in Go, backed by PostgreSQL and Redis. It moves money between accounts using a double-entry ledger and is designed so that retried or concurrent requests can never charge a customer twice. It exposes both a REST and a gRPC API, and its throughput under contention is measured with k6.

> **Status: in progress.** Milestones 1–3 (core API, ledger integrity and gRPC, performance work) are complete. Reconciliation, fault injection, and observability are still planned. See the [Roadmap](#roadmap).

## Features

- **Idempotent payment creation** — every request carries an `Idempotency-Key`. A retry with the same key and body returns the original result instead of creating a second payment. Reusing a key with a *different* body is rejected.
- **Transactional money movement** — each payment runs as a single SQL transaction: claim the key, lock both accounts, check funds, write the payment, ledger entries and balances, commit. Any failure rolls the whole thing back.
- **Double-entry ledger** — every successful payment writes one debit and one credit row. The ledger is append-only.
- **Deadlock avoidance** — the two accounts in a payment are always locked in a fixed order (lowest id first), so opposite-direction payments between the same accounts cannot deadlock.
- **Money as integer cents** — amounts are stored as `BIGINT` minor units, never floats.
- **Failed payments are recorded** — an insufficient-funds attempt is stored as a `FAILED` payment (HTTP 402) and moves no money.
- **REST and gRPC APIs** — both are thin layers over the same service code, so the money logic exists in one place.
- **Ledger invariant checker** — verifies, inside one read-only snapshot, that every successful payment has exactly one matching debit and credit, that failed payments moved no money, and that total debits equal total credits.
- **Per-account concurrency limiter** — caps how many in-flight payments may touch one account, so requests queued behind a hot account wait in Go instead of holding database connections.
- **Redis rate limiter** — a token bucket per `X-Client-Id` on `POST /v1/payments`, returning `429` with a `Retry-After` header. It runs as one atomic Lua script in Redis and fails open if Redis is unavailable.
- **Redis read cache** — cache-aside caching for `GET /v1/payments/{id}`. Stored payments never change, so there is nothing to invalidate. Misses and Redis failures fall through to Postgres.

## API

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/payments` | Create a payment (idempotent) |
| `GET` | `/v1/payments/{id}` | Fetch a payment by id |
| `GET` | `/healthz` | Liveness and database check |

gRPC (port `9090`): `payment.v1.PaymentService` with `CreatePayment` and `GetPayment`. The contract is in `proto/payment/v1/payment.proto`.

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
| `429` | Client over its rate limit (when rate limiting is enabled) |
| `500` | Internal error (details are logged, not returned) |

## Architecture

    REST (:8080)                gRPC (:9090)
        │                           │
        ▼                           ▼
    internal/api              internal/grpcapi      translate requests, map errors to status codes
        └──────────────┬────────────┘
                       ▼
               internal/payments          idempotency, account locking, ledger, invariants
                  │            │
                  ▼            ▼
             PostgreSQL     Redis (optional: rate limiter, read cache)

Correctness is enforced by the database, not by application code. The primary key on `(client_id, idempotency_key)` guarantees that only one concurrent request wins, and `SELECT ... FOR UPDATE` serializes access to account balances. Redis is used only for rate limiting and read caching; if it is unavailable, payments still work.

## Performance

All numbers come from the k6 scripts in `loadtest/`, with raw results saved in `loadtest/results/`. Each figure is the mean of 3 runs of 20 seconds at 50 concurrent users, with a connection pool of 16.

**Test machine:** one Windows PC running the Go server, k6, PostgreSQL 17 and Redis 7 (both in Docker) together. These are relative before-and-after measurements on a synthetic workload, not production capacity numbers.

### Hot-account contention

The *hot* workload sends every payment between the same two accounts, so every transaction fights for the same row locks. The *spread* workload gives each user a private pair of accounts.

| Workload | Original | Optimized | Change |
|---|---|---|---|
| Hot, throughput | 298 req/s | **603 req/s** | 2.0× |
| Hot, p95 latency | 193 ms | **86 ms** | −55% |
| Hot, max latency | 247 ms | 98 ms | −60% |
| Spread, throughput | 1,704 req/s | **2,745 req/s** | +61% |
| Spread, p95 latency | 34 ms | 20 ms | −40% |

Two changes produced this: collapsing the transaction's writes into a single SQL statement (shorter lock hold time), and the per-account concurrency limiter (see below). No requests failed in any run.

### Cost of the rate limiter

With the limit set high enough that nothing is rejected, the extra Redis round trip per request cost about **1.4%** of throughput on the hot workload and about **7%** on the spread workload.

### Read cache

`GET /v1/payments/{id}` over 200 payments: **13,480 → 17,967 req/s (+33%)**, median latency 3.34 → 2.47 ms. The p95 improved only slightly (4.81 → 4.54 ms), because a primary-key lookup in a local Postgres is already fast. This is close to the cache's best case (a tiny working set and an essentially 100% hit rate), so real gains depend on the hit rate.

## Notable Design Decisions

- **Why a per-account limiter instead of a smaller connection pool.** The first hot-account fix attempt was shrinking the pool. Tail latency improved sharply (p95 233 ms at 16 connections vs 91 ms at 2), because connections waiting on a row lock do no useful work. But a pool of 2 cut the spread workload from 2,714 to 716 req/s, so no single pool size suited both workloads. Limiting concurrency per account keeps the full pool for everyone else while a hot account queues in Go. Accounts are acquired in id order, the same rule that prevents database deadlocks.
- **The limiter is per process.** If several server instances ran, each would have its own queues, so some lock waiting at the database could return. Coordinating across instances would need a shared store such as Redis.
- **The concurrency test was verified by breaking the code.** To confirm the opposite-direction test really catches lock-ordering bugs, the ordered locking was temporarily replaced with payer-first locking. The test then failed with Postgres `deadlock detected` errors, and passed again once the change was reverted.
- **The rate limiter is REST-only for now.** It is applied as HTTP middleware on `POST /v1/payments`. The gRPC `CreatePayment` call is not rate limited yet.
- **Features that change performance are opt-in.** Rate limiting and caching are off unless their environment variables are set, which keeps load-test baselines reproducible.

## Tech Stack

- Go 1.27 (standard library `net/http`), gRPC and Protocol Buffers
- PostgreSQL 17 via `pgx` v5 (`pgxpool`)
- Redis 7 via `go-redis` v9 (rate limiter and read cache)
- Docker Compose for local infrastructure
- k6 for load testing

## Repository Structure

    .
    ├── cmd/server/            # Server entry point (REST, gRPC, /healthz)
    ├── internal/
    │   ├── api/               # REST handlers
    │   ├── grpcapi/           # gRPC server
    │   ├── gen/paymentpb/     # Generated protobuf code (do not edit)
    │   ├── payments/          # Payment logic, account limiter, invariants, tests
    │   ├── paymentcache/      # Redis read cache
    │   └── ratelimit/         # Redis token-bucket rate limiter and HTTP middleware
    ├── proto/payment/v1/      # gRPC contract
    ├── db/
    │   ├── schema.sql         # Tables, enum, indexes (auto-applied on first container start)
    │   └── seed.sql           # Two demo accounts
    ├── loadtest/              # k6 scripts, reset and seed SQL, sweep scripts, saved results
    ├── docker-compose.yml     # Postgres and Redis
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

REST listens on `:8080` and gRPC on `:9090`.

**4. Create a payment**

```
curl.exe -i -X POST http://localhost:8080/v1/payments -H "X-Client-Id: demo" -H "Idempotency-Key: demo-1" -H "Content-Type: application/json" --data "@loadtest/pay-1000.json"
```

Run it twice: the first call returns `201`, the second returns `200` with `Idempotent-Replayed: true` and no second payment.

### Configuration

All settings are environment variables. Rate limiting and caching are off unless enabled.

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://payments:payments@localhost:5432/payments` | Postgres connection. Add `?pool_max_conns=N` to size the pool. |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection |
| `RATE_LIMIT_PER_SEC` | unset (off) | Sustained requests per second allowed per client |
| `RATE_LIMIT_BURST` | 2 × the rate | Burst size of the token bucket |
| `PAYMENT_CACHE_TTL` | unset (off) | Enables the read cache, for example `5m` |
| `ACCOUNT_CONCURRENCY` | `2` | In-flight payments allowed per account |

> The Postgres and Redis credentials in `docker-compose.yml` are throwaway **local development** values.

## Testing

```
go test ./... -p 1
```

Tests need the Docker containers running. Use `-p 1`: the packages share one development database, and running them in parallel would let them wipe each other's data. Covered so far:

- 100 concurrent identical requests create exactly one payment
- Same idempotency key with a different body is rejected
- Insufficient funds produces a `FAILED` payment and moves no money
- 400 concurrent requests in both directions between the same two accounts (each sent twice) cause no deadlocks, create exactly the expected payments, leave exact balances, and keep total money constant
- The ledger invariant checker passes on healthy data and detects a deliberately deleted ledger row
- The gRPC lifecycle: create, idempotent replay, key reuse, get, error codes, recorded failure
- The per-account limiter blocks, isolates accounts, and cleans up after itself
- The token-bucket rate limiter limits, refills, and returns `429` with `Retry-After`
- The read cache hits, does not cache not-found results, and expires entries

> The tests truncate the payment tables and reset account balances, so run them against the local dev database only.

## Load Testing

Requires [k6](https://grafana.com/docs/k6/latest/set-up/install-k6/).

```
# one-time: create 100 payer and 100 merchant accounts for the spread workload
Get-Content loadtest\bench-seed.sql | docker compose exec -T postgres psql -U payments -d payments

# hot-account workload, 3 runs, with the server started by the script
powershell -ExecutionPolicy Bypass -File loadtest\pool-sweep.ps1 -Scenario hot -Sizes 16 -Runs 3 -Tag _mytest

# also available: -Scenario spread, -Scenario get
```

The script resets the data before each run, starts its own server build, and prints throughput and latency. Make sure no other server is running on port 8080 first. Raw results are saved to `loadtest/results/`.

## Roadmap

### Milestone 1 — Core payment API ✅
- [x] Project skeleton, Docker Compose, schema, seed data
- [x] Idempotent, transactional `CreatePayment` with double-entry ledger
- [x] Concurrency, key-reuse, and insufficient-funds tests
- [x] `POST /v1/payments` and `GET /v1/payments/{id}`

### Milestone 2 — gRPC and ledger integrity ✅
- [x] gRPC interface alongside REST
- [x] Ledger invariant checks, with a test proving they catch corruption
- [x] Concurrent opposite-direction payment test, verified by breaking the lock ordering

### Milestone 3 — Performance ✅
- [x] k6 load tests with saved before-and-after results
- [x] Hot-account contention found and fixed (single-statement writes and a per-account limiter)
- [x] Redis token-bucket rate limiter
- [x] Redis read cache

### Milestone 4 — Reliability
- [ ] Reconciliation job that detects and reports ledger and payment mismatches
- [ ] Fault injection (crashes and failures mid-transaction)
- [ ] Metrics and dashboards with Prometheus and Grafana
- [ ] Rate limiting for the gRPC API

### Milestone 5 — Polish
- [ ] Architecture and design-decision write-up
- [ ] Final README with measured results
