package handler

import (
	"encoding/json"
	"net/http"

	"example.com/payments/internal/service"
)

// PaymentHandler serves the HTTP surface. Once the route analyzer lands, each
// registration below becomes a route node with a routes_to edge to its
// handler, so `le graph impact "/payments/{id}" --change route` finds the
// handler, the service beneath it, and any client with a literal prefix.
type PaymentHandler struct {
	svc *service.PaymentService
}

func NewPaymentHandler(svc *service.PaymentService) *PaymentHandler {
	return &PaymentHandler{svc: svc}
}

func (h *PaymentHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /payments/{id}", h.get)
	mux.HandleFunc("POST /payments/{id}/refund", h.refund)
}

func (h *PaymentHandler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(p)
}

func (h *PaymentHandler) refund(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Amount int64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := h.svc.Refund(r.Context(), r.PathValue("id"), body.Amount); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
