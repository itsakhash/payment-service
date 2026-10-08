// Command grpcprobe fires a burst of CreatePayment gRPC calls at a local server
// and prints the status code of each, to check rate limiting by hand.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/itsakhash/payment-service/internal/gen/paymentpb"
)

func main() {
	conn, err := grpc.NewClient("localhost:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	client := paymentpb.NewPaymentServiceClient(conn)

	run := time.Now().UnixNano()
	counts := map[codes.Code]int{}
	for i := 1; i <= 12; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := client.CreatePayment(ctx, &paymentpb.CreatePaymentRequest{
			ClientId:       "grpc-demo",
			IdempotencyKey: fmt.Sprintf("probe-%d-%d", run, i),
			PayerAccountId: 1,
			PayeeAccountId: 2,
			AmountMinor:    100,
			Currency:       "USD",
		})
		cancel()
		code := status.Code(err)
		counts[code]++
		fmt.Printf("call %2d: %s\n", i, code)
	}
	fmt.Println("totals:", counts)
}
