package ratelimit

import (
	"log"
	"math"
	"net/http"
	"strconv"
)

// Middleware rate-limits POST /v1/payments per X-Client-Id.
// Everything else (reads, health checks) passes straight through.
// If the limiter itself fails (for example Redis is down), the request is
// allowed and the error is logged: a broken limiter must not stop payments.
func Middleware(l *Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/payments" {
			next.ServeHTTP(w, r)
			return
		}
		clientID := r.Header.Get("X-Client-Id")
		if clientID == "" {
			next.ServeHTTP(w, r) // the handler rejects a missing header itself
			return
		}

		ok, retry, err := l.Allow(r.Context(), clientID)
		if err != nil {
			log.Printf("rate limiter unavailable, allowing request: %v", err)
			next.ServeHTTP(w, r)
			return
		}
		if !ok {
			secs := int(math.Ceil(retry.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
