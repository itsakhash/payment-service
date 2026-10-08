package ratelimit

import (
	"context"
	"log"
	"math"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryInterceptor rate-limits one gRPC method (fullMethod, for example
// "/payment.v1.PaymentService/CreatePayment") per client, using the same Redis
// token bucket as the REST middleware, so a client gets one shared budget
// across both protocols.
//
// clientID extracts the client id from the request. If it returns "", the call
// passes through and the handler rejects it itself, as the REST path does.
// If the limiter fails (for example Redis is down) the call is allowed and the
// error is logged: a broken limiter must not stop payments.
func UnaryInterceptor(l *Limiter, fullMethod string, clientID func(req any) string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod != fullMethod {
			return handler(ctx, req)
		}
		id := clientID(req)
		if id == "" {
			return handler(ctx, req)
		}

		ok, retry, err := l.Allow(ctx, id)
		if err != nil {
			log.Printf("rate limiter unavailable, allowing gRPC call: %v", err)
			return handler(ctx, req)
		}
		if !ok {
			secs := int(math.Ceil(retry.Seconds()))
			if secs < 1 {
				secs = 1
			}
			_ = grpc.SetTrailer(ctx, metadata.Pairs("retry-after", strconv.Itoa(secs)))
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded, retry after %ds", secs)
		}
		return handler(ctx, req)
	}
}
