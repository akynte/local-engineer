// Package worker holds the background jobs. Each runs on a timer, does one
// pass, and reports what it changed; none of them holds state between passes,
// so a restart loses nothing but timing.
package worker

import (
	"context"
	"fmt"
	"time"

	"example.com/warehouse/internal/audit"
	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/inventory"
	"example.com/warehouse/internal/store"
)

// Reconciler corrects reservations that outlived their orders. A reservation
// belonging to a cancelled order holds stock nobody can buy, which is the most
// expensive kind of bug this service has.
type Reconciler struct {
	orders    store.OrderStore
	stock     store.StockStore
	inventory *inventory.Service
	audit     audit.Log
	now       func() time.Time
}

// NewReconciler builds a reconciler.
func NewReconciler(orders store.OrderStore, stock store.StockStore,
	inv *inventory.Service, log audit.Log) *Reconciler {
	return &Reconciler{orders: orders, stock: stock, inventory: inv, audit: log, now: time.Now}
}

// Report is what one pass changed.
type Report struct {
	OrdersExamined    int
	ReservationsFreed int
	Discrepancies     []string
}

// RunOnce makes a single reconciliation pass.
func (r *Reconciler) RunOnce(ctx context.Context) (Report, error) {
	var rep Report

	cancelled, err := r.orders.ListOrdersByState(ctx, domain.StateCancelled)
	if err != nil {
		return rep, err
	}
	rep.OrdersExamined = len(cancelled)

	for _, order := range cancelled {
		for _, line := range order.Lines {
			st, err := r.stock.GetStock(ctx, line.SKU)
			if err != nil {
				rep.Discrepancies = append(rep.Discrepancies,
					fmt.Sprintf("order %s line %s: %v", order.ID, line.SKU, err))
				continue
			}
			if st.Reserved <= 0 {
				continue
			}
			if err := r.inventory.Release(ctx, line.SKU, line.Quantity); err != nil {
				rep.Discrepancies = append(rep.Discrepancies,
					fmt.Sprintf("releasing %s: %v", line.SKU, err))
				continue
			}
			rep.ReservationsFreed += line.Quantity
		}
	}

	_ = r.audit.Record(ctx, audit.Event{
		Actor: "reconciler", Action: "reconcile.pass", Subject: "stock",
		Detail: map[string]string{
			"orders_examined": fmt.Sprint(rep.OrdersExamined),
			"freed":           fmt.Sprint(rep.ReservationsFreed),
		},
	})
	return rep, nil
}

// Run loops until the context is cancelled.
func (r *Reconciler) Run(ctx context.Context, every time.Duration) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := r.RunOnce(ctx); err != nil {
				return err
			}
		}
	}
}
