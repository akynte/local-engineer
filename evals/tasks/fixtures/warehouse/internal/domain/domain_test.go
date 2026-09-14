package domain

import (
	"errors"
	"testing"
)

func TestMoneyAddRefusesMixedCurrency(t *testing.T) {
	a := NewMoney(100, GBP)
	b := NewMoney(100, EUR)
	if _, err := a.Add(b); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("got %v, want ErrCurrencyMismatch", err)
	}
}

func TestParseSKUNormalises(t *testing.T) {
	got, err := ParseSKU("  ab-1234 ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "AB-1234" {
		t.Fatalf("got %q, want AB-1234", got)
	}
}

func TestParseSKURejectsJunk(t *testing.T) {
	for _, in := range []string{"", "AB1234", "ABCDE-1234", "AB-12"} {
		if _, err := ParseSKU(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestStateMachineRefusesIllegalMoves(t *testing.T) {
	o := Order{State: StateDraft}
	if err := o.Transition(StateShipped); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("draft to shipped was allowed: %v", err)
	}
	if err := o.Transition(StateReserved); err != nil {
		t.Fatalf("draft to reserved was refused: %v", err)
	}
}

func TestSubtotalSumsLines(t *testing.T) {
	o := Order{Currency: GBP, Lines: []OrderLine{
		{Quantity: 2, UnitPrice: NewMoney(150, GBP)},
		{Quantity: 1, UnitPrice: NewMoney(200, GBP)},
	}}
	got, err := o.Subtotal()
	if err != nil {
		t.Fatal(err)
	}
	if got.Minor != 500 {
		t.Fatalf("subtotal = %d, want 500", got.Minor)
	}
}

func TestSpecialHandlingCategories(t *testing.T) {
	if !CategoryHazardous.RequiresSpecialHandling() {
		t.Error("hazardous should require special handling")
	}
	if CategoryGeneral.RequiresSpecialHandling() {
		t.Error("general should not require special handling")
	}
}
