package keystore

import "sync"

// Memory is a keystore held in memory, for tests.
type Memory struct {
	mu    sync.Mutex
	Items map[string]MemoryItem
}

// MemoryItem is one stored key and its recipient.
type MemoryItem struct{ Key, Recipient string }

func NewMemory() *Memory { return &Memory{Items: map[string]MemoryItem{}} }

func (m *Memory) Get(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.Items[id]
	if !ok {
		return "", ErrNotFound
	}
	return item.Key, nil
}

func (m *Memory) Put(id, key, recipient string) error {
	if err := checkItem(id, key, recipient); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Items[id]; ok {
		return ErrExists
	}
	m.Items[id] = MemoryItem{Key: key, Recipient: recipient}
	return nil
}

func (m *Memory) Describe(id string) string   { return "keystore item " + Service + "/" + id }
func (m *Memory) LookupCommand(string) string { return "" }
