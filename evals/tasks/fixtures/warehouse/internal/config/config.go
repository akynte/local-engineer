// Package config reads the service's settings from the environment. Every
// setting has a default that is safe in development and an override that
// production is expected to set.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the whole of the service's configuration.
type Config struct {
	ListenAddr      string
	TaxRateBasis    int
	BulkMinUnits    int
	BulkPercentOff  int
	ReorderLevel    int
	ReconcileEvery  int
	DefaultCurrency string
	AuditRetainDays int
}

// Default returns the development configuration.
func Default() Config {
	return Config{
		ListenAddr:      "127.0.0.1:8080",
		TaxRateBasis:    2000,
		BulkMinUnits:    10,
		BulkPercentOff:  5,
		ReorderLevel:    5,
		ReconcileEvery:  60,
		DefaultCurrency: "GBP",
		AuditRetainDays: 90,
	}
}

// Load reads the environment over the defaults.
func Load() (Config, error) {
	c := Default()
	var problems []string

	if v := os.Getenv("WAREHOUSE_LISTEN_ADDR"); v != "" {
		c.ListenAddr = v
	}
	if v := os.Getenv("WAREHOUSE_TAX_RATE_BASIS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 10000 {
			problems = append(problems, "WAREHOUSE_TAX_RATE_BASIS must be 0..10000")
		} else {
			c.TaxRateBasis = n
		}
	}
	if v := os.Getenv("WAREHOUSE_BULK_MIN_UNITS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			problems = append(problems, "WAREHOUSE_BULK_MIN_UNITS must be a positive integer")
		} else {
			c.BulkMinUnits = n
		}
	}
	if v := os.Getenv("WAREHOUSE_BULK_PERCENT_OFF"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 100 {
			problems = append(problems, "WAREHOUSE_BULK_PERCENT_OFF must be 0..100")
		} else {
			c.BulkPercentOff = n
		}
	}
	if v := os.Getenv("WAREHOUSE_REORDER_LEVEL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			problems = append(problems, "WAREHOUSE_REORDER_LEVEL must be a non-negative integer")
		} else {
			c.ReorderLevel = n
		}
	}
	if v := os.Getenv("WAREHOUSE_RECONCILE_EVERY_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			problems = append(problems, "WAREHOUSE_RECONCILE_EVERY_SECONDS must be a positive integer")
		} else {
			c.ReconcileEvery = n
		}
	}
	if v := os.Getenv("WAREHOUSE_DEFAULT_CURRENCY"); v != "" {
		c.DefaultCurrency = strings.ToUpper(v)
	}

	if len(problems) > 0 {
		return c, fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return c, nil
}
