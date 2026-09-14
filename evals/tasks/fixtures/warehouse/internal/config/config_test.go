package config

import "testing"

func TestDefaultIsUsable(t *testing.T) {
	c := Default()
	if c.ListenAddr == "" || c.TaxRateBasis <= 0 {
		t.Fatalf("default config is not usable: %+v", c)
	}
}

func TestLoadRejectsOutOfRangeTax(t *testing.T) {
	t.Setenv("WAREHOUSE_TAX_RATE_BASIS", "99999")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for an out-of-range tax rate")
	}
}
