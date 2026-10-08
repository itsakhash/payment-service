// Package metrics defines the Prometheus metrics the service exposes.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by method, route pattern and status code.",
	}, []string{"method", "route", "status"})

	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency by method and route pattern.",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"method", "route"})

	// Payments counts CreatePayment outcomes: succeeded, failed, replayed,
	// rejected (bad request / unknown account / key reuse) and error (internal).
	Payments = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "payments_total",
		Help: "CreatePayment outcomes.",
	}, []string{"outcome"})
)

func init() {
	prometheus.MustRegister(HTTPRequests, HTTPDuration, Payments)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Middleware records request count and latency. The route label is the mux
// pattern (e.g. "GET /v1/payments/{id}"), not the raw path, so payment ids do
// not create unbounded label values.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern // filled in by ServeMux while routing
		if route == "" {
			route = "unmatched"
		}
		HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		HTTPDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}
