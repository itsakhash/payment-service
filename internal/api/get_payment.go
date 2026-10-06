package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/itsakhash/payment-service/internal/payments"
)

func (h Handler) getPayment(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetPayment(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, payments.ErrInvalidRequest):
		http.Error(w, "invalid payment id", http.StatusBadRequest)
		return
	case errors.Is(err, payments.ErrPaymentNotFound):
		http.Error(w, "payment not found", http.StatusNotFound)
		return
	case err != nil:
		log.Printf("get payment: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"paymentId": p.PaymentID,
		"status":    p.Status,
	})
}
