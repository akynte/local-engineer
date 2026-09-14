package domain

import (
	"errors"
	"regexp"
	"strings"
)

// SKU identifies a stock-keeping unit.
type SKU string

var skuPattern = regexp.MustCompile(`^[A-Z]{2,4}-[0-9]{4,6}$`)

// ErrInvalidSKU is returned for a code that does not match the house format.
var ErrInvalidSKU = errors.New("domain: invalid SKU")

// ParseSKU validates and normalises a code.
func ParseSKU(raw string) (SKU, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if !skuPattern.MatchString(s) {
		return "", ErrInvalidSKU
	}
	return SKU(s), nil
}

// String renders the code.
func (s SKU) String() string { return string(s) }

// Category groups items for reporting and for shipping rules.
type Category string

const (
	CategoryGeneral    Category = "general"
	CategoryFragile    Category = "fragile"
	CategoryHazardous  Category = "hazardous"
	CategoryPerishable Category = "perishable"
	CategoryOversized  Category = "oversized"
)

// RequiresSpecialHandling reports whether a category cannot go by standard
// carrier. Shipping and the packing worker both depend on this answer, so it
// lives on the domain type rather than being re-derived in each.
func (c Category) RequiresSpecialHandling() bool {
	return c == CategoryHazardous || c == CategoryOversized
}

// Dimensions are in millimetres.
type Dimensions struct {
	LengthMM int
	WidthMM  int
	HeightMM int
}

// VolumeCM3 returns the volume in cubic centimetres.
func (d Dimensions) VolumeCM3() int {
	return (d.LengthMM * d.WidthMM * d.HeightMM) / 1000
}

// Item is a thing the warehouse stocks.
type Item struct {
	SKU        SKU
	Name       string
	Category   Category
	UnitPrice  Money
	WeightGram int
	Dimensions Dimensions
	// Discontinued items can be shipped from remaining stock but never
	// reordered.
	Discontinued bool
}

// Validate reports whether the item is usable.
func (i Item) Validate() error {
	if i.SKU == "" {
		return ErrInvalidSKU
	}
	if !i.UnitPrice.Valid() {
		return errors.New("domain: item has no valid price")
	}
	if i.WeightGram <= 0 {
		return errors.New("domain: item has no weight")
	}
	return nil
}
