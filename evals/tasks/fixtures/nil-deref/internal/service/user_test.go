package service

import "testing"

// The visible test only covers the happy path, which is why the bug survived.
func TestEmailFound(t *testing.T) {
	store := NewMemoryStore()
	store.Add(&User{ID: "u1", Email: "a@example.com"})

	svc := NewUserService(store)
	got, err := svc.Email("u1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "a@example.com" {
		t.Fatalf("got %q", got)
	}
}
