package domain

import (
	"errors"
	"time"
)

// OrderID identifies an order.
type OrderID string

// OrderState is where an order is in its lifecycle.
type OrderState string

const (
	StateDraft     OrderState = "draft"
	StateReserved  OrderState = "reserved"
	StatePaid      OrderState = "paid"
	StatePacked    OrderState = "packed"
	StateShipped   OrderState = "shipped"
	StateCancelled OrderState = "cancelled"
	StateRefunded  OrderState = "refunded"
)

// ErrIllegalTransition is returned when a state change is not allowed.
var ErrIllegalTransition = errors.New("domain: illegal state transition")

// transitions is the state machine. A state absent from this map is terminal.
var transitions = map[OrderState][]OrderState{
	StateDraft:    {StateReserved, StateCancelled},
	StateReserved: {StatePaid, StateCancelled},
	StatePaid:     {StatePacked, StateRefunded},
	StatePacked:   {StateShipped, StateRefunded},
	StateShipped:  {StateRefunded},
}

// CanTransitionTo reports whether a move is legal.
func (s OrderState) CanTransitionTo(next OrderState) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Terminal reports whether no further transition is possible.
func (s OrderState) Terminal() bool { return len(transitions[s]) == 0 }

// OrderLine is one item on an order.
type OrderLine struct {
	SKU       SKU
	Quantity  int
	UnitPrice Money
}

// LineTotal returns the cost of this line before any discount.
func (l OrderLine) LineTotal() Money { return l.UnitPrice.Times(l.Quantity) }

// Order is a customer order.
type Order struct {
	ID         OrderID
	CustomerID string
	State      OrderState
	Lines      []OrderLine
	Currency   Currency
	PlacedAt   time.Time
	// ShippingZone is resolved at placement and frozen, so a later change to
	// zone definitions does not silently reprice a shipped order.
	ShippingZone string
}

// Subtotal sums the lines. It returns an error when the order mixes
// currencies, which a partially-migrated import can produce.
func (o Order) Subtotal() (Money, error) {
	total := Money{Currency: o.Currency}
	for _, l := range o.Lines {
		lt := l.LineTotal()
		sum, err := total.Add(lt)
		if err != nil {
			return Money{}, err
		}
		total = sum
	}
	return total, nil
}

// TotalUnits counts every unit across every line.
func (o Order) TotalUnits() int {
	n := 0
	for _, l := range o.Lines {
		n += l.Quantity
	}
	return n
}

// Transition moves the order, refusing an illegal move.
func (o *Order) Transition(next OrderState) error {
	if !o.State.CanTransitionTo(next) {
		return ErrIllegalTransition
	}
	o.State = next
	return nil
}
