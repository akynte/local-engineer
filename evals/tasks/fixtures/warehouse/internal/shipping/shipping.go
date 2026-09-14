// Package shipping resolves a destination to a zone and a zone plus a parcel
// to a rate. Zones are data rather than code because they change on commercial
// terms, not engineering ones.
package shipping

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/store"
)

// ErrNoZone is returned for a destination no zone covers.
var ErrNoZone = errors.New("shipping: no zone covers that destination")

// ErrNoCarrier is returned when nothing can carry the parcel, which happens
// for hazardous goods outside the domestic zone.
var ErrNoCarrier = errors.New("shipping: no carrier will take this parcel")

// Zone is a shipping region.
type Zone struct {
	Name          string
	CountryCodes  []string
	BaseRateMinor int64
	PerKgMinor    int64
	// SpecialHandling reports whether carriers in this zone accept parcels
	// needing special handling.
	SpecialHandling bool
}

// Covers reports whether a country is in the zone.
func (z Zone) Covers(country string) bool {
	want := strings.ToUpper(strings.TrimSpace(country))
	for _, c := range z.CountryCodes {
		if c == want {
			return true
		}
	}
	return false
}

// Parcel is what is being shipped.
type Parcel struct {
	WeightGram         int
	VolumeCM3          int
	RequiresSpecial    bool
	DeclaredValue      domain.Money
	ContainsPerishable bool
}

// Service computes shipping.
type Service struct {
	zones []Zone
	items store.ItemStore
}

// New builds a shipping service.
func New(items store.ItemStore, zones ...Zone) *Service {
	return &Service{items: items, zones: zones}
}

// ZoneFor resolves a country to a zone.
func (s *Service) ZoneFor(country string) (Zone, error) {
	for _, z := range s.zones {
		if z.Covers(country) {
			return z, nil
		}
	}
	return Zone{}, fmt.Errorf("%w: %s", ErrNoZone, country)
}

// ZoneByName looks a zone up by the name frozen onto an order.
func (s *Service) ZoneByName(name string) (Zone, error) {
	for _, z := range s.zones {
		if z.Name == name {
			return z, nil
		}
	}
	return Zone{}, fmt.Errorf("%w: zone %q", ErrNoZone, name)
}

// ParcelFor builds the parcel an order would ship as, by looking up each item.
func (s *Service) ParcelFor(ctx context.Context, order domain.Order) (Parcel, error) {
	var p Parcel
	p.DeclaredValue = domain.Money{Currency: order.Currency}
	for _, line := range order.Lines {
		item, err := s.items.GetItem(ctx, line.SKU)
		if err != nil {
			return Parcel{}, fmt.Errorf("shipping: line %s: %w", line.SKU, err)
		}
		p.WeightGram += item.WeightGram * line.Quantity
		p.VolumeCM3 += item.Dimensions.VolumeCM3() * line.Quantity
		if item.Category.RequiresSpecialHandling() {
			p.RequiresSpecial = true
		}
		if item.Category == domain.CategoryPerishable {
			p.ContainsPerishable = true
		}
		value, err := p.DeclaredValue.Add(line.LineTotal())
		if err != nil {
			return Parcel{}, err
		}
		p.DeclaredValue = value
	}
	return p, nil
}

// Rate prices a parcel into a zone.
func (s *Service) Rate(zone Zone, p Parcel, currency domain.Currency) (domain.Money, error) {
	if p.RequiresSpecial && !zone.SpecialHandling {
		return domain.Money{}, fmt.Errorf("%w: zone %s", ErrNoCarrier, zone.Name)
	}
	kg := (p.WeightGram + 999) / 1000
	minor := zone.BaseRateMinor + zone.PerKgMinor*int64(kg)
	return domain.Money{Minor: minor, Currency: currency}, nil
}

// Quote resolves the zone from the order and prices it in one call, which is
// what the API handler wants.
func (s *Service) Quote(ctx context.Context, order domain.Order) (domain.Money, error) {
	zone, err := s.ZoneByName(order.ShippingZone)
	if err != nil {
		return domain.Money{}, err
	}
	parcel, err := s.ParcelFor(ctx, order)
	if err != nil {
		return domain.Money{}, err
	}
	return s.Rate(zone, parcel, order.Currency)
}

// DefaultZones are the zones the service ships with.
func DefaultZones() []Zone {
	return []Zone{
		{Name: "domestic", CountryCodes: []string{"GB"}, BaseRateMinor: 395, PerKgMinor: 100, SpecialHandling: true},
		{Name: "eu", CountryCodes: []string{"IE", "FR", "DE", "NL", "ES", "IT"}, BaseRateMinor: 995, PerKgMinor: 250},
		{Name: "world", CountryCodes: []string{"US", "CA", "AU", "NZ", "JP"}, BaseRateMinor: 1995, PerKgMinor: 600},
	}
}
