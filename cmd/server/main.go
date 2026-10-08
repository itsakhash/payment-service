package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsakhash/payment-service/internal/api"
	"github.com/itsakhash/payment-service/internal/payments"

	"net"

	"google.golang.org/grpc"

	"github.com/itsakhash/payment-service/internal/gen/paymentpb"
	"github.com/itsakhash/payment-service/internal/grpcapi"

	"strconv"

	"github.com/redis/go-redis/v9"

	"github.com/itsakhash/payment-service/internal/ratelimit"

	"github.com/itsakhash/payment-service/internal/paymentcache"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/itsakhash/payment-service/internal/metrics"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://payments:payments@localhost:5432/payments"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("create pool: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("ping db: %v", err)
	}

	svc := payments.NewService(pool)

	// Optional background reconciliation. Uses context.Background() on purpose:
	// ctx above has a 5-second timeout and would stop the loop.
	if iv, perr := time.ParseDuration(os.Getenv("RECONCILE_INTERVAL")); perr == nil && iv > 0 {
		go svc.RunReconcileLoop(context.Background(), iv)
		log.Printf("reconciliation every %s", iv)
	}

	if ttl, perr := time.ParseDuration(os.Getenv("PAYMENT_CACHE_TTL")); perr == nil && ttl > 0 {
		cacheURL := os.Getenv("REDIS_URL")
		if cacheURL == "" {
			cacheURL = "redis://localhost:6379/0"
		}
		cacheOpt, cerr := redis.ParseURL(cacheURL)
		if cerr != nil {
			log.Fatal(cerr)
		}
		svc.WithCache(paymentcache.New(redis.NewClient(cacheOpt), ttl))
		log.Printf("payment read cache on (ttl %s)", ttl)
	}
	handler := api.New(svc)
	go func() {
		lis, err := net.Listen("tcp", ":9090")
		if err != nil {
			log.Fatal(err)
		}
		gs := grpc.NewServer()
		paymentpb.RegisterPaymentServiceServer(gs, grpcapi.New(svc))
		log.Println("grpc listening on :9090")
		log.Fatal(gs.Serve(lis))
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	handler.Register(mux)

	var root http.Handler = mux
	if rps, _ := strconv.ParseFloat(os.Getenv("RATE_LIMIT_PER_SEC"), 64); rps > 0 {
		burst, _ := strconv.Atoi(os.Getenv("RATE_LIMIT_BURST"))
		if burst <= 0 {
			burst = int(rps * 2)
		}
		if burst < 1 {
			burst = 1
		}
		redisURL := os.Getenv("REDIS_URL")
		if redisURL == "" {
			redisURL = "redis://localhost:6379/0"
		}
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			log.Fatal(err)
		}
		root = ratelimit.Middleware(ratelimit.New(redis.NewClient(opt), burst, rps), mux)
		log.Printf("rate limiting on: %.0f req/s per client, burst %d", rps, burst)
	}

	root = metrics.Middleware(root) // outermost, so rate-limited (429) requests are counted too

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Println("listening on :8080")
	log.Fatal(srv.ListenAndServe())
}
