package payments

import (
	"context"
	"os"
	"strconv"
	"sync"
)

const defaultAccountConcurrency = 2

// accountConcurrencyFromEnv lets the per-account limit be tuned without a code change.
func accountConcurrencyFromEnv() int {
	if v, err := strconv.Atoi(os.Getenv("ACCOUNT_CONCURRENCY")); err == nil && v > 0 {
		return v
	}
	return defaultAccountConcurrency
}

type limiterEntry struct {
	slots chan struct{}
	refs  int
}

// accountLimiter caps how many in-flight payments may touch one account.
// Waiting happens here, in Go, so waiters hold no database connection.
type accountLimiter struct {
	mu      sync.Mutex
	entries map[int64]*limiterEntry
	perAcct int
}

func newAccountLimiter(perAccount int) *accountLimiter {
	return &accountLimiter{entries: map[int64]*limiterEntry{}, perAcct: perAccount}
}

func (l *accountLimiter) acquire(ctx context.Context, id int64) (func(), error) {
	l.mu.Lock()
	e := l.entries[id]
	if e == nil {
		e = &limiterEntry{slots: make(chan struct{}, l.perAcct)}
		l.entries[id] = e
	}
	e.refs++
	l.mu.Unlock()

	done := func() {
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.entries, id) // idle accounts don't accumulate memory
		}
		l.mu.Unlock()
	}

	select {
	case e.slots <- struct{}{}:
		return func() {
			<-e.slots
			done()
		}, nil
	case <-ctx.Done():
		done()
		return nil, ctx.Err()
	}
}

// acquirePair takes slots on both accounts, lowest id first, so two payments
// in opposite directions can never wait on each other forever.
func (l *accountLimiter) acquirePair(ctx context.Context, a, b int64) (func(), error) {
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	releaseLo, err := l.acquire(ctx, lo)
	if err != nil {
		return nil, err
	}
	releaseHi, err := l.acquire(ctx, hi)
	if err != nil {
		releaseLo()
		return nil, err
	}
	return func() {
		releaseHi()
		releaseLo()
	}, nil
}
