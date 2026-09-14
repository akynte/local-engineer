// Package orders owns the order lifecycle. It is the only package that moves
// an order between states, and every move it makes is audited.
package orders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"example.com/warehouse/internal/audit"
	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/inventory"
	"example.com/warehouse/internal/notify"
	"example.com/warehouse/internal/pricing"
	"example.com/warehouse/internal/shipping"
	"example.com/warehouse/internal/store"
)

// ErrEmptyOrder is returned when an order has no lines.
var ErrEmptyOrder = errors.New("orders: an order must have at least one line")

// Service runs the order lifecycle.
type Service struct {
	orders    store.OrderStore
	items     store.ItemStore
	inventory *inventory.Service
	pricing   *pricing.Calculator
	shipping  *shipping.Service
	audit     audit.Log
	notifier  *notify.Notifier
	now       func() time.Time
}

// New builds an order service.
func New(
	orders store.OrderStore,
	items store.ItemStore,
	inv *inventory.Service,
	price *pricing.Calculator,
	ship *shipping.Service,
	log audit.Log,
	notifier *notify.Notifier,
) *Service {
	return &Service{
		orders: orders, items: items, inventory: inv, pricing: price,
		shipping: ship, audit: log, notifier: notifier, now: time.Now,
	}
}

// PlaceRequest is what a caller submits.
type PlaceRequest struct {
	OrderID     domain.OrderID
	CustomerID  string
	Currency    domain.Currency
	CountryCode string
	Lines       []RequestLine
}

// RequestLine is one requested item and quantity. The price is not supplied by
// the caller: it is read from the catalogue, so a client cannot choose it.
type RequestLine struct {
	SKU      domain.SKU
	Quantity int
}

// Place creates an order, reserves stock for it and prices it.
func (s *Service) Place(ctx context.Context, req PlaceRequest) (domain.Order, pricing.Breakdown, error) {
	if len(req.Lines) == 0 {
		return domain.Order{}, pricing.Breakdown{}, ErrEmptyOrder
	}
	zone, err := s.shipping.ZoneFor(req.CountryCode)
	if err != nil {
		return domain.Order{}, pricing.Breakdown{}, err
	}

	order := domain.Order{
		ID: req.OrderID, CustomerID: req.CustomerID, State: domain.StateDraft,
		Currency: req.Currency, PlacedAt: s.now(), ShippingZone: zone.Name,
	}
	for _, rl := range req.Lines {
		item, err := s.items.GetItem(ctx, rl.SKU)
		if err != nil {
			return domain.Order{}, pricing.Breakdown{}, fmt.Errorf("orders: %s: %w", rl.SKU, err)
		}
		if item.UnitPrice.Currency != req.Currency {
			return domain.Order{}, pricing.Breakdown{}, domain.ErrCurrencyMismatch
		}
		order.Lines = append(order.Lines, domain.OrderLine{
			SKU: rl.SKU, Quantity: rl.Quantity, UnitPrice: item.UnitPrice,
		})
	}

	for _, line := range order.Lines {
		if err := s.inventory.Reserve(ctx, line.SKU, line.Quantity); err != nil {
			return domain.Order{}, pricing.Breakdown{}, err
		}
	}
	if err := order.Transition(domain.StateReserved); err != nil {
		return domain.Order{}, pricing.Breakdown{}, err
	}

	quote, err := s.shipping.Quote(ctx, order)
	if err != nil {
		return domain.Order{}, pricing.Breakdown{}, err
	}
	breakdown, err := s.pricing.Price(ctx, order, quote)
	if err != nil {
		return domain.Order{}, pricing.Breakdown{}, err
	}

	if err := s.orders.PutOrder(ctx, order); err != nil {
		return domain.Order{}, pricing.Breakdown{}, err
	}
	s.record(ctx, "order.placed", string(order.ID), map[string]string{
		"customer": order.CustomerID,
		"total":    breakdown.Total.String(),
	})
	return order, breakdown, nil
}

// Cancel stops an order and releases its stock.
func (s *Service) Cancel(ctx context.Context, id domain.OrderID, reason string) error {
	order, err := s.orders.GetOrder(ctx, id)
	if err != nil {
		return err
	}
	if err := order.Transition(domain.StateCancelled); err != nil {
		return err
	}
	for _, line := range order.Lines {
		if err := s.inventory.Release(ctx, line.SKU, line.Quantity); err != nil {
			return err
		}
	}
	if err := s.orders.PutOrder(ctx, order); err != nil {
		return err
	}
	s.record(ctx, "order.cancelled", string(order.ID), map[string]string{"reason": reason})
	if err := s.notifier.OrderCancelled(ctx, order, reason); err != nil {
		s.record(ctx, "notify.failed", string(order.ID), map[string]string{"error": err.Error()})
	}
	return nil
}

// MarkPaid moves a reserved order to paid.
func (s *Service) MarkPaid(ctx context.Context, id domain.OrderID) error {
	return s.move(ctx, id, domain.StatePaid, "order.paid", nil)
}

// Pack moves a paid order to packed.
func (s *Service) Pack(ctx context.Context, id domain.OrderID) error {
	return s.move(ctx, id, domain.StatePacked, "order.packed", nil)
}

// Ship despatches a packed order, consuming the reservation.
func (s *Service) Ship(ctx context.Context, id domain.OrderID, tracking string) error {
	order, err := s.orders.GetOrder(ctx, id)
	if err != nil {
		return err
	}
	if err := order.Transition(domain.StateShipped); err != nil {
		return err
	}
	for _, line := range order.Lines {
		if err := s.inventory.Consume(ctx, line.SKU, line.Quantity); err != nil {
			return err
		}
	}
	if err := s.orders.PutOrder(ctx, order); err != nil {
		return err
	}
	s.record(ctx, "order.shipped", string(order.ID), map[string]string{"tracking": tracking})
	if err := s.notifier.OrderShipped(ctx, order, tracking); err != nil {
		s.record(ctx, "notify.failed", string(order.ID), map[string]string{"error": err.Error()})
	}
	return nil
}

func (s *Service) move(ctx context.Context, id domain.OrderID, to domain.OrderState,
	action string, detail map[string]string) error {
	order, err := s.orders.GetOrder(ctx, id)
	if err != nil {
		return err
	}
	if err := order.Transition(to); err != nil {
		return err
	}
	if err := s.orders.PutOrder(ctx, order); err != nil {
		return err
	}
	s.record(ctx, action, string(order.ID), detail)
	return nil
}

func (s *Service) record(ctx context.Context, action, subject string, detail map[string]string) {
	_ = s.audit.Record(ctx, audit.Event{
		Actor: "orders", Action: action, Subject: subject, Detail: detail,
	})
}
