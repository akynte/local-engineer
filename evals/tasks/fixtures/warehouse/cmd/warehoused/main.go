// Command warehoused runs the warehouse service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/warehouse/internal/api"
	"example.com/warehouse/internal/audit"
	"example.com/warehouse/internal/config"
	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/inventory"
	"example.com/warehouse/internal/notify"
	"example.com/warehouse/internal/orders"
	"example.com/warehouse/internal/pricing"
	"example.com/warehouse/internal/shipping"
	"example.com/warehouse/internal/store"
	"example.com/warehouse/internal/worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	mem := store.NewMemory()
	log.Printf("starting with tax rate %d basis points", cfg.TaxRateBasis)

	inv := inventory.New(mem, mem)
	auditLog := audit.NewMemoryLog()
	notifier := notify.New(notify.NewRecorder())
	ship := shipping.New(mem, shipping.DefaultZones()...)
	price := pricing.New(mem, pricing.TaxRate(cfg.TaxRateBasis),
		pricing.BulkDiscount{MinUnits: cfg.BulkMinUnits, PercentOffBase: cfg.BulkPercentOff},
	)
	orderSvc := orders.New(mem, mem, inv, price, ship, auditLog, notifier)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.NewServer(orderSvc, inv, mem).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reconciler := worker.NewReconciler(mem, mem, inv, auditLog)
	go func() {
		if err := reconciler.Run(ctx, time.Duration(cfg.ReconcileEvery)*time.Second); err != nil &&
			!isCancelled(err) {
			log.Printf("reconciler stopped: %v", err)
		}
	}()

	watcher := worker.NewRestockWatcher(inv, auditLog, cfg.ReorderLevel)
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.ReconcileEvery) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := watcher.RunOnce(ctx); err != nil {
					log.Printf("restock watcher: %v", err)
				} else if n > 0 {
					log.Printf("%d SKU(s) need reordering", n)
				}
			}
		}
	}()

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("listening on %s (default currency %s)", cfg.ListenAddr,
		domain.Currency(cfg.DefaultCurrency))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

func isCancelled(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
