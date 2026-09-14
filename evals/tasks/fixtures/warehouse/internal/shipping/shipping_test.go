package shipping

import (
	"context"
	"errors"
	"testing"

	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/store"
)

func TestZoneForResolvesCountry(t *testing.T) {
	s := New(store.NewMemory(), DefaultZones()...)
	z, err := s.ZoneFor("gb")
	if err != nil {
		t.Fatal(err)
	}
	if z.Name != "domestic" {
		t.Fatalf("zone = %s, want domestic", z.Name)
	}
}

func TestZoneForRejectsUnknown(t *testing.T) {
	s := New(store.NewMemory(), DefaultZones()...)
	if _, err := s.ZoneFor("XX"); !errors.Is(err, ErrNoZone) {
		t.Fatalf("got %v, want ErrNoZone", err)
	}
}

func TestRateRoundsWeightUpToTheKilo(t *testing.T) {
	s := New(store.NewMemory(), DefaultZones()...)
	z, _ := s.ZoneFor("GB")
	got, err := s.Rate(z, Parcel{WeightGram: 1200}, domain.GBP)
	if err != nil {
		t.Fatal(err)
	}
	// base 395 + 2kg at 100 = 595
	if got.Minor != 595 {
		t.Fatalf("rate = %d, want 595", got.Minor)
	}
}

func TestRateRefusesSpecialHandlingWhereUnsupported(t *testing.T) {
	s := New(store.NewMemory(), DefaultZones()...)
	z, _ := s.ZoneFor("FR")
	_, err := s.Rate(z, Parcel{WeightGram: 500, RequiresSpecial: true}, domain.GBP)
	if !errors.Is(err, ErrNoCarrier) {
		t.Fatalf("got %v, want ErrNoCarrier", err)
	}
}

func TestParcelForSumsWeights(t *testing.T) {
	mem := store.NewMemory()
	ctx := context.Background()
	_ = mem.PutItem(ctx, domain.Item{
		SKU: "AB-1234", WeightGram: 300, UnitPrice: domain.NewMoney(100, domain.GBP),
		Dimensions: domain.Dimensions{LengthMM: 100, WidthMM: 100, HeightMM: 100},
	})
	s := New(mem, DefaultZones()...)
	p, err := s.ParcelFor(ctx, domain.Order{
		Currency: domain.GBP,
		Lines:    []domain.OrderLine{{SKU: "AB-1234", Quantity: 3, UnitPrice: domain.NewMoney(100, domain.GBP)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.WeightGram != 900 {
		t.Fatalf("weight = %d, want 900", p.WeightGram)
	}
}
