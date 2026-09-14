// Package inventory owns stock levels and reservations. It is the only package
// allowed to change a Stock row: orders ask it to reserve, the reconciliation
// worker asks it to correct, and nothing else writes.
package inventory

import (
	"context"
	"errors"
	"fmt"

	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/store"
)

// ErrInsufficientStock is returned when a reservation cannot be met. The API
// turns it into a 409 rather than a 500, so it is a named error.
var ErrInsufficientStock = errors.New("inventory: insufficient stock")

// Service manages stock.
type Service struct {
	stock store.StockStore
	items store.ItemStore
}

// New builds an inventory service.
func New(stock store.StockStore, items store.ItemStore) *Service {
	return &Service{stock: stock, items: items}
}

// Available reports what can still be promised for a SKU.
func (s *Service) Available(ctx context.Context, sku domain.SKU) (int, error) {
	st, err := s.stock.GetStock(ctx, sku)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return st.Available(), nil
}

// Reserve holds stock for an order. It is called once per line.
func (s *Service) Reserve(ctx context.Context, sku domain.SKU, quantity int) error {
	if quantity <= 0 {
		return fmt.Errorf("inventory: quantity must be positive, got %d", quantity)
	}
	st, err := s.stock.GetStock(ctx, sku)
	if err != nil {
		return err
	}
	if st.Available() < quantity {
		return fmt.Errorf("%w: %s has %d available, %d requested",
			ErrInsufficientStock, sku, st.Available(), quantity)
	}
	st.Reserved += quantity
	return s.stock.PutStock(ctx, st)
}

// Release returns reserved stock to the available pool, used when an order is
// cancelled before it ships.
func (s *Service) Release(ctx context.Context, sku domain.SKU, quantity int) error {
	st, err := s.stock.GetStock(ctx, sku)
	if err != nil {
		return err
	}
	st.Reserved -= quantity
	if st.Reserved < 0 {
		st.Reserved = 0
	}
	return s.stock.PutStock(ctx, st)
}

// Consume converts a reservation into a despatch: the units leave the building,
// so both the on-hand and the reserved counts fall.
func (s *Service) Consume(ctx context.Context, sku domain.SKU, quantity int) error {
	st, err := s.stock.GetStock(ctx, sku)
	if err != nil {
		return err
	}
	st.OnHand -= quantity
	st.Reserved -= quantity
	if st.Reserved < 0 {
		st.Reserved = 0
	}
	return s.stock.PutStock(ctx, st)
}

// Receive adds newly delivered units.
func (s *Service) Receive(ctx context.Context, sku domain.SKU, quantity int) error {
	item, err := s.items.GetItem(ctx, sku)
	if err != nil {
		return err
	}
	if item.Discontinued {
		return fmt.Errorf("inventory: %s is discontinued and cannot be restocked", sku)
	}
	st, err := s.stock.GetStock(ctx, sku)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		st = store.Stock{SKU: sku}
	}
	st.OnHand += quantity
	return s.stock.PutStock(ctx, st)
}

// ReorderReport lists SKUs whose available count has fallen below a threshold
// and which are still orderable.
func (s *Service) ReorderReport(ctx context.Context, threshold int) ([]domain.SKU, error) {
	rows, err := s.stock.ListStock(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.SKU
	for _, row := range rows {
		if row.Available() >= threshold {
			continue
		}
		item, err := s.items.GetItem(ctx, row.SKU)
		if err != nil {
			continue
		}
		if item.Discontinued {
			continue
		}
		out = append(out, row.SKU)
	}
	return out, nil
}
