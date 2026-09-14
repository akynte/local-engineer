package repository

import (
	"context"
	"errors"
)

// ErrNotFound is returned when no payment matches.
var ErrNotFound = errors.New("payment not found")

// Payment mirrors the payments table in migrations/001_payments.sql. The
// schema analyzer will link this type to that table with a reads_schema edge.
type Payment struct {
	ID       string
	Amount   int64
	Currency string
	Status   string
}

// Store is the persistence port. An interface here is what makes the
// interface-to-implementation edge worth having: impact analysis on a method
// signature should find every implementation, not only the call sites.
type Store interface {
	Load(ctx context.Context, id string) (*Payment, error)
	Save(ctx context.Context, p *Payment) error
}

// PostgresStore is the production implementation.
type PostgresStore struct{ dsn string }

func NewPostgresStore(dsn string) *PostgresStore { return &PostgresStore{dsn: dsn} }

func (s *PostgresStore) Load(ctx context.Context, id string) (*Payment, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	return &Payment{ID: id, Amount: 1000, Currency: "GBP", Status: "settled"}, nil
}

func (s *PostgresStore) Save(ctx context.Context, p *Payment) error {
	if p == nil || p.ID == "" {
		return errors.New("payment requires an id")
	}
	return nil
}
