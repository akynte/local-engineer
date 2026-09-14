package worker

import (
	"context"
	"fmt"

	"example.com/warehouse/internal/audit"
	"example.com/warehouse/internal/inventory"
)

// RestockWatcher reports SKUs that have fallen below the reorder level. It does
// not place orders itself: buying is a human decision, and the watcher exists
// to make sure the human is told.
type RestockWatcher struct {
	inventory *inventory.Service
	audit     audit.Log
	threshold int
}

// NewRestockWatcher builds a watcher.
func NewRestockWatcher(inv *inventory.Service, log audit.Log, threshold int) *RestockWatcher {
	return &RestockWatcher{inventory: inv, audit: log, threshold: threshold}
}

// RunOnce makes one pass and returns how many SKUs need reordering.
func (w *RestockWatcher) RunOnce(ctx context.Context) (int, error) {
	skus, err := w.inventory.ReorderReport(ctx, w.threshold)
	if err != nil {
		return 0, err
	}
	for _, sku := range skus {
		_ = w.audit.Record(ctx, audit.Event{
			Actor: "restock", Action: "restock.needed", Subject: sku.String(),
			Detail: map[string]string{"threshold": fmt.Sprint(w.threshold)},
		})
	}
	return len(skus), nil
}
