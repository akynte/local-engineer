package service

import "errors"

// ErrNotFound is returned when no user matches.
var ErrNotFound = errors.New("user not found")

// User is a shop customer.
type User struct {
	ID    string
	Email string
}

// Store loads users.
type Store interface {
	Find(id string) *User
}

// UserService is the business layer over Store.
type UserService struct {
	store Store
}

func NewUserService(store Store) *UserService { return &UserService{store: store} }

// Email returns the user's email address.
//
// It panics when the user does not exist, because Find returns nil and this
// dereferences it without checking.
func (s *UserService) Email(id string) (string, error) {
	u := s.store.Find(id)
	return u.Email, nil
}
