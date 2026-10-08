package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsakhash/payment-service/internal/payments"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://payments:payments@localhost:5432/payments"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	violations, err := payments.NewService(pool).Reconcile(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if len(violations) == 0 {
		fmt.Println("reconciliation OK: no violations")
		return
	}
	for _, v := range violations {
		fmt.Printf("VIOLATION %s: %s\n", v.Check, v.Detail)
	}
	os.Exit(1)
}
