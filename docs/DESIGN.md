# Design Notes

How the payment service works, why it is built this way, and what was measured. Every number below came from a test or load run in this repository; the limitations section lists what was *not* built or tested.

## 1. Goals

- Move money between accounts so that **no payment is ever applied twice, lost, or half-applied**, even with retries, concurrent requests, and crashes.
- Keep the ledger auditable: the books must be checkable against balances at any time.
- Stay fast on a **hot account** (many payments touching the same account), which is where naive implementations fall over.

Non-goals: authentication, multi-currency conversion, refunds and reversals, multi-node deployment.

## 2. Data model

| Table | Purpose |
|---|---|
| `accounts` | Balance per account, in integer minor units (cents). Never floats. |
| `payments` | One row per attempt, with status `SUCCEEDED` or `FAILED`. |
| `ledger_entries` | Append-only double entry: each successful payment writes one debit (`D`) and one credit (`C`) row. |
| `idempotency_keys` | Primary key `(client_id, idem_key)`, plus a hash of the request and the resulting payment id. |
| `reconciliation_baselines` | Per-account "implied opening balance", used to detect drift (section 8). |

## 3. Creating a payment

`CreatePayment` runs as **one database transaction**:

1. Claim the idempotency key with `INSERT ... ON CONFLICT DO NOTHING`.
2. If the key already existed, replay the stored result (or reject it if the request body differs).
3. Lock both accounts with `SELECT ... FOR UPDATE ... ORDER BY id`.
4. Check currency and funds.
5. Write the payment, both ledger rows, both balance updates, and link the key, in **a single SQL statement** (a data-modifying CTE).
6. Commit.

A failed attempt (insufficient funds) is recorded as a `FAILED` payment, returns HTTP 402, and moves no money.

## 4. Idempotency

Correctness comes from the database, not application checks. The primary key on `(client_id, idem_key)` means that when 100 goroutines send the same key at once, exactly one insert wins and the rest wait for it, then replay its result. The request hash catches a client reusing a key with a different body, which is rejected rather than replayed.

## 5. Deadlock avoidance

Two payments in opposite directions between the same accounts would deadlock if each locked its payer first. Accounts are therefore always locked lowest id first. This was verified by test: 400 concurrent two-way payments pass, and deliberately removing the ordering makes the test fail with a deadlock.

## 6. The hot-account problem

All payments touching one account serialize on that account's row lock, so throughput is limited by **how long each transaction holds the lock**. Two changes, each measured:

1. **One statement instead of six round trips.** Collapsing the writes into a single CTE shortens the lock hold time.
2. **A per-account in-process concurrency limiter** (default 2 in-flight payments per account, acquired in id order *before* a database connection is taken). After change 1, tail latency regressed, because many transactions queued on the lock while holding pool connections. The limiter makes them wait in the application instead, so they don't tie up the pool.

A pool-size sweep showed that **no single global pool size was best for both a hot-account workload and a spread-out one**, which is why the limiter is per account rather than a pool tweak.

| Workload (50 VUs, 20 s, pool 16, mean of 3 runs) | Before | After |
|---|---|---|
| Hot account: throughput | 298 req/s | 603 req/s |
| Hot account: p95 latency | 193 ms | 86 ms |
| Hot account: max latency | 247 ms | 98 ms |
| Spread accounts: throughput | 1,704 req/s | 2,745 req/s |
| Spread accounts: p95 latency | 34 ms | 20 ms |

"Before" was re-measured by building the earlier commit in a separate `git worktree`.

## 7. Redis: rate limiting and read cache

**Rate limiter.** A token bucket implemented as one atomic Lua script, using Redis's own clock so multiple server instances agree on time. It is keyed by `X-Client-Id` (REST) or `client_id` (gRPC) and shares one budget across both protocols. Over the limit returns HTTP 429 with `Retry-After`, or gRPC `ResourceExhausted`. It **fails open**: if Redis is down, payments are allowed and the error is logged, because a broken limiter must not stop payments. Measured cost: about 1.4% throughput on the hot workload and 7% on the spread workload.

**Read cache.** Cache-aside for `GET /v1/payments/{id}`. Payments are immutable once written, so there is no invalidation problem. Misses are not cached (no negative caching), and cache failures fall through to the database. Measured: reads went from 13,480 to 17,967 req/s (+33%), median latency 3.34 to 2.47 ms.

## 8. Integrity checking

- **Invariants** (run in a `REPEATABLE READ` read-only transaction for a consistent snapshot): every `SUCCEEDED` payment has exactly one debit and one credit of the right amount; `FAILED` payments have no ledger rows; total debits equal total credits.
- **Reconciliation** adds balance drift detection. Because accounts are seeded with an opening balance that isn't in the ledger, each account stores an *implied opening balance* (`balance - ledger net`) on first sight. Any later change to that value means money moved without a matching ledger entry. It also flags idempotency keys that never got a payment.
- Reconciliation runs as a CLI (`go run ./cmd/reconcile`, exit code 1 on violations) and as an optional background loop (`RECONCILE_INTERVAL`) that publishes a violation gauge.

## 9. Failure handling

A test hook can fail `CreatePayment` at four points: after the key claim, after the account locks, before commit, and after commit. Tests show:

- A failure at any point **before commit** leaves nothing behind (no payment, key, ledger row, or balance change), and a retry with the same key then succeeds exactly once.
- A failure **after commit** (the client never sees the answer) followed by a retry replays the stored result: one payment, charged once.
- The crash test was verified by sabotage: claiming the key outside the transaction makes it fail.
- A **chaos test** runs 40 concurrent payments with 15% random injected failures per point and random `pg_terminate_backend` kills of in-transaction connections, with clients retrying under the same key. In one run it took 86 attempts, 41 injected errors, and 6 connection-kill rounds; the end state was exactly 40 payments, exactly 4,000 cents moved, and no invariant violations.

## 10. Observability

Prometheus metrics at `/metrics`: HTTP request count and latency by route pattern and status, payment outcomes (`succeeded`, `failed`, `replayed`, `rejected`, `error`), and reconciliation results. A provisioned Grafana dashboard (`docker-compose.monitoring.yml`) shows traffic, p95 latency, and reconciliation health. Route labels use the mux pattern, not the raw path, to keep label cardinality bounded.

## 11. How results were measured

All load numbers come from k6 against a single Windows machine running k6, the server, Postgres, and Redis together (Postgres and Redis in Docker). Absolute numbers would differ on separate machines; the before/after comparisons are the meaningful part. Each configuration was run three times.

## 12. Known limitations

- **Single node.** One Postgres, no replicas, no failover. The per-account limiter is in-process, so it limits per server instance, not across a fleet.
- **No authentication.** `X-Client-Id` is self-declared, so the rate limiter is only as trustworthy as that header. gRPC has no TLS. `/metrics` is exposed on the public port.
- **Retries rely on the client.** A response lost after commit is only safe because the client retries with the same idempotency key.
- **Reconciliation can't validate the opening balance.** It detects drift *since* the first run, not whether the starting balance was right.
- **Fault injection is in-process.** It simulates crashes with injected errors and killed database connections, not with killing the Go process or the database container.
- **Not built:** refunds and reversals, currency conversion, ledger partitioning, a multi-instance deployment, alerting rules.
