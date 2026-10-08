package payments

import (
	"context"
	"log"
	"time"

	"github.com/itsakhash/payment-service/internal/metrics"
)

// RunReconcileLoop runs Reconcile immediately and then every interval until ctx
// is cancelled, publishing the result as metrics and logging each violation.
func (s *Service) RunReconcileLoop(ctx context.Context, interval time.Duration) {
	run := func() {
		violations, err := s.Reconcile(ctx)
		if err != nil {
			metrics.ReconcileRuns.WithLabelValues("error").Inc()
			log.Printf("reconcile: run failed: %v", err)
			return
		}
		metrics.ReconcileViolations.Set(float64(len(violations)))
		metrics.ReconcileLastSuccess.SetToCurrentTime()
		if len(violations) == 0 {
			metrics.ReconcileRuns.WithLabelValues("clean").Inc()
			return
		}
		metrics.ReconcileRuns.WithLabelValues("violations").Inc()
		for _, v := range violations {
			log.Printf("reconcile: VIOLATION %s: %s", v.Check, v.Detail)
		}
	}

	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
