package inventory

import (
	"context"
	"errors"
	"testing"

	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/store"
)

func setup(t *testing.T) (*Service, *store.Memory) {
	t.Helper()
	mem := store.NewMemory()
	ctx := context.Background()
	item := domain.Item{
		SKU: "AB-1234", Name: "Widget", Category: domain.CategoryGeneral,
		UnitPrice: domain.NewMoney(500, domain.GBP), WeightGram: 250,
	}
	if err := mem.PutItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := mem.PutStock(ctx, store.Stock{SKU: "AB-1234", OnHand: 10}); err != nil {
		t.Fatal(err)
	}
	return New(mem, mem), mem
}

func TestReserveReducesAvailable(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()
	if err := svc.Reserve(ctx, "AB-1234", 3); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Available(ctx, "AB-1234")
	if err != nil {
		t.Fatal(err)
	}
	if got != 7 {
		t.Fatalf("available = %d, want 7", got)
	}
}

func TestReserveRefusesWhenShort(t *testing.T) {
	svc, _ := setup(t)
	err := svc.Reserve(context.Background(), "AB-1234", 99)
	if !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("got %v, want ErrInsufficientStock", err)
	}
}

func TestConsumeDropsOnHand(t *testing.T) {
	svc, mem := setup(t)
	ctx := context.Background()
	if err := svc.Reserve(ctx, "AB-1234", 4); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(ctx, "AB-1234", 4); err != nil {
		t.Fatal(err)
	}
	st, err := mem.GetStock(ctx, "AB-1234")
	if err != nil {
		t.Fatal(err)
	}
	if st.OnHand != 6 || st.Reserved != 0 {
		t.Fatalf("on hand %d reserved %d, want 6 and 0", st.OnHand, st.Reserved)
	}
}
