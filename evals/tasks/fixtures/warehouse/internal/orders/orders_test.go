package orders

import (
	"context"
	"errors"
	"testing"

	"example.com/warehouse/internal/audit"
	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/inventory"
	"example.com/warehouse/internal/notify"
	"example.com/warehouse/internal/pricing"
	"example.com/warehouse/internal/shipping"
	"example.com/warehouse/internal/store"
)

func newService(t *testing.T) (*Service, *store.Memory) {
	t.Helper()
	mem := store.NewMemory()
	ctx := context.Background()
	if err := mem.PutItem(ctx, domain.Item{
		SKU: "AB-1234", Name: "Widget", Category: domain.CategoryGeneral,
		UnitPrice: domain.NewMoney(1000, domain.GBP), WeightGram: 500,
		Dimensions: domain.Dimensions{LengthMM: 100, WidthMM: 100, HeightMM: 50},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.PutStock(ctx, store.Stock{SKU: "AB-1234", OnHand: 20}); err != nil {
		t.Fatal(err)
	}
	inv := inventory.New(mem, mem)
	return New(mem, mem, inv,
		pricing.New(mem, 2000),
		shipping.New(mem, shipping.DefaultZones()...),
		audit.NewMemoryLog(),
		notify.New(notify.NewRecorder()),
	), mem
}

func request() PlaceRequest {
	return PlaceRequest{
		OrderID: "o1", CustomerID: "c1", Currency: domain.GBP, CountryCode: "GB",
		Lines: []RequestLine{{SKU: "AB-1234", Quantity: 2}},
	}
}

func TestPlaceReservesStock(t *testing.T) {
	svc, mem := newService(t)
	ctx := context.Background()
	order, _, err := svc.Place(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	if order.State != domain.StateReserved {
		t.Fatalf("state = %s, want reserved", order.State)
	}
	st, err := mem.GetStock(ctx, "AB-1234")
	if err != nil {
		t.Fatal(err)
	}
	if st.Reserved != 2 {
		t.Fatalf("reserved = %d, want 2", st.Reserved)
	}
}

func TestPlaceRejectsEmptyOrder(t *testing.T) {
	svc, _ := newService(t)
	req := request()
	req.Lines = nil
	if _, _, err := svc.Place(context.Background(), req); !errors.Is(err, ErrEmptyOrder) {
		t.Fatalf("got %v, want ErrEmptyOrder", err)
	}
}

func TestPlaceTakesPriceFromTheCatalogue(t *testing.T) {
	svc, _ := newService(t)
	order, _, err := svc.Place(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if order.Lines[0].UnitPrice.Minor != 1000 {
		t.Fatalf("unit price = %d, want the catalogue price 1000", order.Lines[0].UnitPrice.Minor)
	}
}

func TestCancelReleasesStock(t *testing.T) {
	svc, mem := newService(t)
	ctx := context.Background()
	if _, _, err := svc.Place(ctx, request()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Cancel(ctx, "o1", "customer changed their mind"); err != nil {
		t.Fatal(err)
	}
	st, err := mem.GetStock(ctx, "AB-1234")
	if err != nil {
		t.Fatal(err)
	}
	if st.Reserved != 0 {
		t.Fatalf("reserved = %d after cancel, want 0", st.Reserved)
	}
}

func TestShipConsumesStock(t *testing.T) {
	svc, mem := newService(t)
	ctx := context.Background()
	if _, _, err := svc.Place(ctx, request()); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkPaid(ctx, "o1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Pack(ctx, "o1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Ship(ctx, "o1", "TRACK123"); err != nil {
		t.Fatal(err)
	}
	st, err := mem.GetStock(ctx, "AB-1234")
	if err != nil {
		t.Fatal(err)
	}
	if st.OnHand != 18 || st.Reserved != 0 {
		t.Fatalf("on hand %d reserved %d, want 18 and 0", st.OnHand, st.Reserved)
	}
}
