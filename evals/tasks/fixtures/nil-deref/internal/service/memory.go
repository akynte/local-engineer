package service

// MemoryStore is the in-memory implementation used by the handler layer.
type MemoryStore struct {
	users map[string]*User
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{users: map[string]*User{}} }

func (m *MemoryStore) Add(u *User) { m.users[u.ID] = u }

// Find returns nil when the user is absent.
func (m *MemoryStore) Find(id string) *User { return m.users[id] }
