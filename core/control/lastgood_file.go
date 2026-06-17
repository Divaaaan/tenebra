package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Divaaaan/tenebra/core/fallback"
)

// lastGoodFile is the name of the JSON file the persistent last-good store keeps
// inside the daemon's config directory.
const lastGoodFile = "lastgood.json"

// fileLastGood is a disk-backed fallback.LastGood: it remembers, per profile,
// the node ID that most recently connected, so the next launch leads the
// fallback walk with what last worked instead of starting from scratch. The map
// is held in memory and rewritten atomically (temp file + rename) on every Set,
// so a crash mid-write never corrupts the file. It is safe for concurrent use —
// the connect loop writes it while other code may read it.
//
// A read or parse failure at open is non-fatal: the store starts empty and
// learns again from the first successful connect, which is strictly better than
// refusing to run because a cache file got mangled.
type fileLastGood struct {
	path string

	mu sync.RWMutex
	m  map[string]string
}

// OpenFileLastGood binds a persistent last-good store to dir, creating the
// directory if needed and loading any existing file. A missing or unreadable
// file yields an empty store rather than an error. The result satisfies
// fallback.LastGood; main installs it on the daemon via SetLastGood.
func OpenFileLastGood(dir string) (fallback.LastGood, error) {
	if dir == "" {
		return nil, errors.New("control: empty last-good directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("control: create last-good dir: %w", err)
	}
	s := &fileLastGood{
		path: filepath.Join(dir, lastGoodFile),
		m:    make(map[string]string),
	}
	s.load()
	return s, nil
}

// load reads the backing file into memory, tolerating a missing or corrupt file
// by leaving the map empty. It runs only at open, before the store is shared.
func (s *fileLastGood) load() {
	data, err := os.ReadFile(s.path)
	if err != nil || len(data) == 0 {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return // corrupt cache: start fresh, relearn on next success
	}
	if m != nil {
		s.m = m
	}
}

// Get returns the last-good node ID for profileID and whether one is recorded.
func (s *fileLastGood) Get(profileID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.m[profileID]
	return id, ok
}

// Set records nodeID as last-good for profileID and persists the whole map. A
// write error is swallowed: last-good is a best-effort optimisation, and a
// failed persist must never break an otherwise-successful connection. The
// in-memory value is updated regardless, so the rest of this run still benefits.
func (s *fileLastGood) Set(profileID, nodeID string) {
	s.mu.Lock()
	if s.m[profileID] == nodeID {
		s.mu.Unlock()
		return // unchanged; skip the disk write
	}
	s.m[profileID] = nodeID
	snapshot := make(map[string]string, len(s.m))
	for k, v := range s.m {
		snapshot[k] = v
	}
	s.mu.Unlock()

	_ = s.save(snapshot)
}

// save writes snapshot to disk atomically: serialise to a temp file in the same
// directory, fsync it, then rename over the target so a reader never sees a
// half-written file. It takes a snapshot rather than reading s.m so it needn't
// hold the lock across the file I/O.
func (s *fileLastGood) save(snapshot map[string]string) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("control: encode last-good: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, lastGoodFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("control: create last-good temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("control: write last-good temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("control: sync last-good temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("control: close last-good temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("control: replace last-good file: %w", err)
	}
	return nil
}

// compile-time assertion that the file store satisfies the interface the daemon
// holds.
var _ fallback.LastGood = (*fileLastGood)(nil)
