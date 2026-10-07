package grpcapi

import (
	"context"
	"errors"
	"log"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/itsakhash/payment-service/internal/gen/paymentpb"
	"github.com/itsakhash/payment-service/internal/payments"
)

// Server exposes payments.Service over gRPC.
type Server struct {
	paymentpb.UnimplementedPaymentServiceServer
	svc *payments.Service
}

func New(svc *payments.Service) *Server {
	return &Server{svc: svc}
}

func (s *Server) CreatePayment(ctx context.Context, in *paymentpb.CreatePaymentRequest) (*paymentpb.CreatePaymentResponse, error) {
	res, err := s.svc.CreatePayment(ctx, payments.CreateRequest{
		ClientID:       in.GetClientId(),
		IdempotencyKey: in.GetIdempotencyKey(),
		PayerAccountID: in.GetPayerAccountId(),
		PayeeAccountID: in.GetPayeeAccountId(),
		AmountMinor:    in.GetAmountMinor(),
		Currency:       in.GetCurrency(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &paymentpb.CreatePaymentResponse{
		PaymentId: string(res.PaymentID),
		Status:    string(res.Status),
		Replayed:  res.Replayed,
	}, nil
}

func (s *Server) GetPayment(ctx context.Context, in *paymentpb.GetPaymentRequest) (*paymentpb.GetPaymentResponse, error) {
	p, err := s.svc.GetPayment(ctx, in.GetPaymentId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &paymentpb.GetPaymentResponse{PaymentId: p.PaymentID, Status: p.Status}, nil
}

// toStatus maps service errors to gRPC status codes.
func toStatus(err error) error {
	switch {
	case errors.Is(err, payments.ErrInvalidRequest):
		return status.Error(codes.InvalidArgument, "invalid request")
	case errors.Is(err, payments.ErrAccountNotFound):
		return status.Error(codes.NotFound, "account not found")
	case errors.Is(err, payments.ErrPaymentNotFound):
		return status.Error(codes.NotFound, "payment not found")
	case errors.Is(err, payments.ErrKeyReused):
		return status.Error(codes.FailedPrecondition, "idempotency key reused with a different request")
	default:
		log.Printf("grpc internal error: %v", err)
		return status.Error(codes.Internal, "internal error")
	}
}
