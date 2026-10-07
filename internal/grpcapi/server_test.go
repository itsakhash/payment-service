package grpcapi

import (
	"context"
	"net"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/itsakhash/payment-service/internal/gen/paymentpb"
	"github.com/itsakhash/payment-service/internal/payments"
)

func setup(t *testing.T) (paymentpb.PaymentServiceClient, int64, int64) {
	t.Helper()
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://payments:payments@localhost:5432/payments"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `TRUNCATE ledger_entries, idempotency_keys, payments`); err != nil {
		t.Fatal(err)
	}
	var payer, merchant int64
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE external_ref = 'payer-1'`).Scan(&payer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE external_ref = 'merchant-1'`).Scan(&merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET balance_minor = 5000 WHERE id = $1`, payer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET balance_minor = 0 WHERE id = $1`, merchant); err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	paymentpb.RegisterPaymentServiceServer(gs, New(payments.NewService(pool)))
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	return paymentpb.NewPaymentServiceClient(conn), payer, merchant
}

func TestGRPCPaymentLifecycle(t *testing.T) {
	ctx := context.Background()
	client, payer, merchant := setup(t)

	mk := func(key string, amount int64) *paymentpb.CreatePaymentRequest {
		return &paymentpb.CreatePaymentRequest{
			ClientId:       "grpc-test",
			IdempotencyKey: key,
			PayerAccountId: payer,
			PayeeAccountId: merchant,
			AmountMinor:    amount,
			Currency:       "USD",
		}
	}
	req := mk("g1", 1000)

	first, err := client.CreatePayment(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.GetPaymentId() == "" || first.GetStatus() != "SUCCEEDED" || first.GetReplayed() {
		t.Fatalf("unexpected first response: %+v", first)
	}

	again, err := client.CreatePayment(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if again.GetPaymentId() != first.GetPaymentId() || !again.GetReplayed() {
		t.Fatalf("retry should replay the same payment, got %+v", again)
	}

	if _, err := client.CreatePayment(ctx, mk("g1", 2000)); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("key reuse with a different body: got %v, want FailedPrecondition", err)
	}

	got, err := client.GetPayment(ctx, &paymentpb.GetPaymentRequest{PaymentId: first.GetPaymentId()})
	if err != nil || got.GetStatus() != "SUCCEEDED" {
		t.Fatalf("GetPayment: %+v, %v", got, err)
	}

	if _, err := client.GetPayment(ctx, &paymentpb.GetPaymentRequest{PaymentId: "not-a-uuid"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed id: got %v, want InvalidArgument", err)
	}
	if _, err := client.GetPayment(ctx, &paymentpb.GetPaymentRequest{PaymentId: "00000000-0000-0000-0000-000000000000"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown id: got %v, want NotFound", err)
	}

	failed, err := client.CreatePayment(ctx, mk("g2", 999999))
	if err != nil {
		t.Fatal(err)
	}
	if failed.GetStatus() != "FAILED" {
		t.Fatalf("insufficient funds should be recorded as FAILED, got %q", failed.GetStatus())
	}
}
