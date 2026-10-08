package payments

// faultPoint names a place inside CreatePayment where a test can inject a failure.
type faultPoint string

const (
	faultAfterKeyClaim faultPoint = "after_key_claim" // idempotency key inserted, nothing else done
	faultAfterLock     faultPoint = "after_lock"      // both accounts locked and read
	faultBeforeCommit  faultPoint = "before_commit"   // all writes done, not yet committed
	faultAfterCommit   faultPoint = "after_commit"    // committed, but the caller never gets the result
)

// inject returns an error if a test hook is installed and asks to fail at p.
// In production s.fault is nil, so this is a no-op.
func (s *Service) inject(p faultPoint) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(p)
}
