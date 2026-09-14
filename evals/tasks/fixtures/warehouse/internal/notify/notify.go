// Package notify sends customer-facing messages. Delivery is best-effort: a
// failed notification must never fail the order it describes, so every method
// returns an error the caller is expected to log rather than propagate.
package notify

import (
	"context"
	"fmt"
	"sync"

	"example.com/warehouse/internal/domain"
)

// Message is one outbound notification.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Recorder is a Sender that keeps what it was given, used in tests and in the
// development environment where nothing should actually be emailed.
type Recorder struct {
	mu   sync.Mutex
	sent []Message
}

// NewRecorder builds a recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Send records a message.
func (r *Recorder) Send(_ context.Context, msg Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, msg)
	return nil
}

// Sent returns what was recorded.
func (r *Recorder) Sent() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Message, len(r.sent))
	copy(out, r.sent)
	return out
}

// Notifier builds and sends the messages the service cares about.
type Notifier struct {
	sender Sender
}

// New builds a notifier.
func New(sender Sender) *Notifier { return &Notifier{sender: sender} }

// OrderShipped tells the customer their order is on its way.
func (n *Notifier) OrderShipped(ctx context.Context, order domain.Order, tracking string) error {
	return n.sender.Send(ctx, Message{
		To:      order.CustomerID,
		Subject: fmt.Sprintf("Order %s has shipped", order.ID),
		Body:    fmt.Sprintf("Your order is on its way. Tracking: %s", tracking),
	})
}

// OrderCancelled tells the customer an order will not be fulfilled.
func (n *Notifier) OrderCancelled(ctx context.Context, order domain.Order, reason string) error {
	return n.sender.Send(ctx, Message{
		To:      order.CustomerID,
		Subject: fmt.Sprintf("Order %s was cancelled", order.ID),
		Body:    fmt.Sprintf("We could not fulfil your order: %s", reason),
	})
}

// BackInStock tells a customer a SKU they wanted is available again.
func (n *Notifier) BackInStock(ctx context.Context, customerID string, sku domain.SKU) error {
	return n.sender.Send(ctx, Message{
		To:      customerID,
		Subject: fmt.Sprintf("%s is back in stock", sku),
		Body:    "The item you asked about is available again.",
	})
}
