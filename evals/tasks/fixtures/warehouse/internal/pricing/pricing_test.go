package pricing

import (
	"context"
	"testing"

	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/store"
)

func order(units int) domain.Order {
	return domain.Order{
		ID: "o1", CustomerID: "c1", Currency: domain.GBP,
		Lines: []domain.OrderLine{{
			SKU: "AB-1234", Quantity: units, UnitPrice: domain.NewMoney(1000, domain.GBP),
		}},
	}
}

func TestPriceAppliesTaxToShippingToo(t *testing.T) {
	c := New(store.NewMemory(), 2000)
	b, err := c.Price(context.Background(), order(1), domain.NewMoney(500, domain.GBP))
	if err != nil {
		t.Fatal(err)
	}
	// 1000 + 500 = 1500, tax at 20% is 300, total 1800.
	if b.Total.Minor != 1800 {
		t.Fatalf("total = %d, want 1800", b.Total.Minor)
	}
}

func TestBulkDiscountAppliesOverThreshold(t *testing.T) {
	c := New(store.NewMemory(), 0, BulkDiscount{MinUnits: 10, PercentOffBase: 10})
	b, err := c.Price(context.Background(), order(10), domain.Money{Currency: domain.GBP})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Discounts) != 1 || b.Discounts[0].Amount.Minor != 1000 {
		t.Fatalf("discounts = %+v, want one of 1000", b.Discounts)
	}
}
