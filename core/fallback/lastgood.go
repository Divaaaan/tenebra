package fallback

import "sync"

// LastGood remembers, per profile, the node ID that most recently connected
// successfully, so a fresh fallback walk can retry it first. The persistent
// implementation lives elsewhere (wired to the profile store); this package
// ships only the in-memory one used by tests and short-lived runs.
type LastGood interface {
	// Get returns the last-good node ID for profileID and whether one is set.
	Get(profileID string) (nodeID string, ok bool)
	// Set records nodeID as last-good for profileID, replacing any prior value.
	Set(profileID, nodeID string)
}

// MemLastGood is an in-memory LastGood. The zero value is not usable; call
// NewMemLastGood. It is safe for concurrent use so a background reconnect loop
// and the UI can touch it at once.
type MemLastGood struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewMemLastGood returns an empty in-memory store.
func NewMemLastGood() *MemLastGood {
	return &MemLastGood{m: make(map[string]string)}
}

func (s *MemLastGood) Get(profileID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.m[profileID]
	return id, ok
}

func (s *MemLastGood) Set(profileID, nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[profileID] = nodeID
}
