package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/itsakhash/payment-service/internal/payments"
)

// Handler is the REST adapter. It contains no business logic.
type Handler struct {
	svc *payments.Service
}

func New(svc *payments.Service) *Handler {
	return &Handler{svc: svc}
}

type createPaymentBody struct {
	PayerAccountID int64  `json:"payerAccountId"`
	PayeeAccountID int64  `json:"payeeAccountId"`
	AmountMinor    int64  `json:"amountMinor"`
	Currency       string `json:"currency"`
}

type paymentResponse struct {
	PaymentID string `json:"paymentId"`
	Status    string `json:"status"`
	Replayed  bool   `json:"replayed"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Register adds this handler's routes to the mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/payments", h.createPayment)
}

func (h *Handler) createPayment(w http.ResponseWriter, r *http.Request) {
	// There is no login yet, so the caller identifies itself with a header.
	// A real system would take this from authentication instead.
	clientID := r.Header.Get("X-Client-Id")
	key := r.Header.Get("Idempotency-Key")
	if clientID == "" || key == "" {
		writeJSON(w, http.StatusBadRequest,
			errorResponse{Error: "X-Client-Id and Idempotency-Key headers are required"})
		return
	}

	// Cap the body at 1 MB and reject fields we do not know about.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body createPaymentBody
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	res, err := h.svc.CreatePayment(r.Context(), payments.CreateRequest{
		ClientID:       clientID,
		IdempotencyKey: key,
		PayerAccountID: body.PayerAccountID,
		PayeeAccountID: body.PayeeAccountID,
		AmountMinor:    body.AmountMinor,
		Currency:       body.Currency,
	})
	if err != nil {
		switch {
		case errors.Is(err, payments.ErrInvalidRequest):
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request"})
		case errors.Is(err, payments.ErrAccountNotFound):
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "account not found"})
		case errors.Is(err, payments.ErrKeyReused):
			writeJSON(w, http.StatusUnprocessableEntity,
				errorResponse{Error: "idempotency key was already used with a different request"})
		default:
			// Log the details for us, but never show internals to the caller.
			log.Printf("create payment: %v", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		}
		return
	}

	// 201 = newly created, 200 = replay of an earlier identical request,
	// 402 = the payment was recorded but failed (for example, insufficient funds).
	code := http.StatusCreated
	if res.Replayed {
		code = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	if res.Status == "FAILED" {
		code = http.StatusPaymentRequired
	}
	writeJSON(w, code, paymentResponse{
		PaymentID: res.PaymentID,
		Status:    res.Status,
		Replayed:  res.Replayed,
	})
}
