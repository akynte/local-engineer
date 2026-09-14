// Package api is the HTTP boundary. It translates domain errors into status
// codes and nothing else: no business rule lives here, because a rule that
// only holds over HTTP does not hold when the worker does the same thing.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/inventory"
	"example.com/warehouse/internal/orders"
	"example.com/warehouse/internal/shipping"
	"example.com/warehouse/internal/store"
)

// Server serves the HTTP API.
type Server struct {
	orders    *orders.Service
	inventory *inventory.Service
	items     store.ItemStore
}

// NewServer builds a server.
func NewServer(o *orders.Service, inv *inventory.Service, items store.ItemStore) *Server {
	return &Server{orders: o, inventory: inv, items: items}
}

// Routes returns the mux.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /items", s.listItems)
	mux.HandleFunc("GET /items/{sku}", s.getItem)
	mux.HandleFunc("GET /stock/{sku}", s.getStock)
	mux.HandleFunc("POST /orders", s.placeOrder)
	mux.HandleFunc("POST /orders/{id}/cancel", s.cancelOrder)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.items.ListItems(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	sku, err := domain.ParseSKU(r.PathValue("sku"))
	if err != nil {
		writeError(w, err)
		return
	}
	item, err := s.items.GetItem(r.Context(), sku)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) getStock(w http.ResponseWriter, r *http.Request) {
	sku, err := domain.ParseSKU(r.PathValue("sku"))
	if err != nil {
		writeError(w, err)
		return
	}
	available, err := s.inventory.Available(r.Context(), sku)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sku": sku, "available": available})
}

type placeOrderRequest struct {
	OrderID     string `json:"order_id"`
	CustomerID  string `json:"customer_id"`
	Currency    string `json:"currency"`
	CountryCode string `json:"country_code"`
	Lines       []struct {
		SKU      string `json:"sku"`
		Quantity int    `json:"quantity"`
	} `json:"lines"`
}

func (s *Server) placeOrder(w http.ResponseWriter, r *http.Request) {
	var req placeOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed body"})
		return
	}
	place := orders.PlaceRequest{
		OrderID:     domain.OrderID(req.OrderID),
		CustomerID:  req.CustomerID,
		Currency:    domain.Currency(req.Currency),
		CountryCode: req.CountryCode,
	}
	for _, l := range req.Lines {
		sku, err := domain.ParseSKU(l.SKU)
		if err != nil {
			writeError(w, err)
			return
		}
		place.Lines = append(place.Lines, orders.RequestLine{SKU: sku, Quantity: l.Quantity})
	}

	order, breakdown, err := s.orders.Place(r.Context(), place)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"order":    order,
		"subtotal": breakdown.Subtotal.String(),
		"shipping": breakdown.Shipping.String(),
		"tax":      breakdown.Tax.String(),
		"total":    breakdown.Total.String(),
	})
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	id := domain.OrderID(r.PathValue("id"))
	if err := s.orders.Cancel(r.Context(), id, r.URL.Query().Get("reason")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError maps a domain or store error to a status code. Every error the
// service defines is represented here; an unrecognised error is a 500, which
// is the honest answer when the cause is unknown.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, domain.ErrInvalidSKU):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sku"})
	case errors.Is(err, domain.ErrCurrencyMismatch):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "currency mismatch"})
	case errors.Is(err, domain.ErrIllegalTransition):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "illegal state transition"})
	case errors.Is(err, inventory.ErrInsufficientStock):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "insufficient stock"})
	case errors.Is(err, shipping.ErrNoZone):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "we do not ship there"})
	case errors.Is(err, shipping.ErrNoCarrier):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no carrier for this parcel"})
	case errors.Is(err, orders.ErrEmptyOrder):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an order needs at least one line"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
