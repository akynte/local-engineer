package store

import (
	"context"
	"sort"
	"sync"

	"example.com/warehouse/internal/domain"
)

// Memory is an in-process implementation of every store interface. It is what
// the service runs against in tests and in the development compose file.
type Memory struct {
	mu     sync.RWMutex
	items  map[domain.SKU]domain.Item
	orders map[domain.OrderID]domain.Order
	stock  map[domain.SKU]Stock
}

// NewMemory builds an empty store.
func NewMemory() *Memory {
	return &Memory{
		items:  map[domain.SKU]domain.Item{},
		orders: map[domain.OrderID]domain.Order{},
		stock:  map[domain.SKU]Stock{},
	}
}

func (m *Memory) GetItem(_ context.Context, sku domain.SKU) (domain.Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[sku]
	if !ok {
		return domain.Item{}, ErrNotFound
	}
	return item, nil
}

func (m *Memory) ListItems(_ context.Context) ([]domain.Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Item, 0, len(m.items))
	for _, i := range m.items {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].SKU < out[b].SKU })
	return out, nil
}

func (m *Memory) PutItem(_ context.Context, item domain.Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[item.SKU] = item
	return nil
}

func (m *Memory) GetOrder(_ context.Context, id domain.OrderID) (domain.Order, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.orders[id]
	if !ok {
		return domain.Order{}, ErrNotFound
	}
	return o, nil
}

func (m *Memory) ListOrdersByState(_ context.Context, state domain.OrderState) ([]domain.Order, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []domain.Order
	for _, o := range m.orders {
		if o.State == state {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (m *Memory) PutOrder(_ context.Context, order domain.Order) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orders[order.ID] = order
	return nil
}

func (m *Memory) GetStock(_ context.Context, sku domain.SKU) (Stock, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.stock[sku]
	if !ok {
		return Stock{}, ErrNotFound
	}
	return s, nil
}

func (m *Memory) PutStock(_ context.Context, stock Stock) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.stock[stock.SKU]
	if exists && stock.Version != current.Version {
		return ErrConflict
	}
	stock.Version++
	m.stock[stock.SKU] = stock
	return nil
}

func (m *Memory) ListStock(_ context.Context) ([]Stock, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Stock, 0, len(m.stock))
	for _, s := range m.stock {
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].SKU < out[b].SKU })
	return out, nil
}
