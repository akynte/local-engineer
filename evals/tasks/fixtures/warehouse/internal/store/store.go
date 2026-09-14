// Package store is the persistence boundary. Everything above it is written
// against these interfaces, so the in-memory implementation used in tests and
// a real database implementation are interchangeable.
package store

import (
	"context"
	"errors"

	"example.com/warehouse/internal/domain"
)

// ErrNotFound is returned when a key is absent. Callers distinguish it from a
// real failure: a missing order is a 404, a broken database is a 500.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when a write would overwrite a newer version.
var ErrConflict = errors.New("store: version conflict")

// ItemStore holds the catalogue.
type ItemStore interface {
	GetItem(ctx context.Context, sku domain.SKU) (domain.Item, error)
	ListItems(ctx context.Context) ([]domain.Item, error)
	PutItem(ctx context.Context, item domain.Item) error
}

// OrderStore holds orders.
type OrderStore interface {
	GetOrder(ctx context.Context, id domain.OrderID) (domain.Order, error)
	ListOrdersByState(ctx context.Context, state domain.OrderState) ([]domain.Order, error)
	PutOrder(ctx context.Context, order domain.Order) error
}

// StockStore holds on-hand and reserved counts per SKU.
type StockStore interface {
	GetStock(ctx context.Context, sku domain.SKU) (Stock, error)
	PutStock(ctx context.Context, stock Stock) error
	ListStock(ctx context.Context) ([]Stock, error)
}

// Stock is the warehouse's count for one SKU.
type Stock struct {
	SKU      domain.SKU
	OnHand   int
	Reserved int
	// Version guards against a lost update when two reservations race.
	Version int
}

// Available is what can still be promised to a new order.
func (s Stock) Available() int { return s.OnHand - s.Reserved }
