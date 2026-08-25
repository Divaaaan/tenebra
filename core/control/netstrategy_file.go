package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// netStrategyFile is the name of the JSON file the per-network strategy cache
// keeps inside the daemon's config directory.
//
// It is its own file rather than another field in settings.json, and the
// distinction is the same one lastgood.json draws: settings.json holds choices
// the user made and would be entitled to see honoured, while this holds the
// result of a measurement the app took on its own. Deleting it costs a pick;
// deleting settings.json costs the user their configuration. Keeping them apart
// also keeps a cache from growing an entry per network inside the file the user
// may reasonably open and edit.
const netStrategyFile = "netstrategy.json"

// netStrategyStore remembers which bypass strategy won on which network. The
// interface is small so the daemon can be handed an in-memory one in tests and
// need not know about the file machinery.
type netStrategyStore interface {
	// Get returns the strategy last measured as working on the given network, and
	// whether one is recorded.
	Get(network string) (string, bool)
	// Set records strategy as what works on the given network.
	Set(network, strategy string)
}

// memNetStrategies is the in-memory default: the cache still works within one
// run of the daemon, it just does not survive a restart. It is what a bare
// daemon in a unit test gets, and what production falls back to if the file
// cannot be opened.
type memNetStrategies struct {
	mu sync.RWMutex
	m  map[string]string
}

func newMemNetStrategies() *memNetStrategies {
	return &memNetStrategies{m: make(map[string]string)}
}

func (s *memNetStrategies) Get(network string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name, ok := s.m[network]
	return name, ok
}

func (s *memNetStrategies) Set(network, strategy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[network] = strategy
}

// fileNetStrategies is a disk-backed netStrategyStore, in the shape
// fileLastGood established: the map is held in memory and the whole file is
// rewritten atomically (temp file + fsync + rename) on every Set, so a crash
// mid-write never leaves a half-written file behind. It is safe for concurrent
// use.
//
// A read or parse failure at open is not an error: the store starts empty and
// relearns from the next pick, which is strictly better than refusing to run
// because a cache file got mangled.
type fileNetStrategies struct {
	path string

	mu sync.RWMutex
	m  map[string]string
}

// OpenFileNetStrategies binds a persistent per-network strategy cache to dir,
// creating the directory if needed and loading any existing file. A missing or
// unreadable file yields an empty store rather than an error. main installs the
// result on the daemon via SetNetStrategies.
func OpenFileNetStrategies(dir string) (netStrategyStore, error) {
	if dir == "" {
		return nil, errors.New("control: empty network-strategy directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("control: create network-strategy dir: %w", err)
	}
	s := &fileNetStrategies{
		path: filepath.Join(dir, netStrategyFile),
		m:    make(map[string]string),
	}
	s.load()
	return s, nil
}

// load reads the backing file into memory, tolerating a missing or corrupt file
// by leaving the map empty. It runs only at open, before the store is shared.
func (s *fileNetStrategies) load() {
	data, err := os.ReadFile(s.path)
	if err != nil || len(data) == 0 {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return // corrupt cache: start fresh, relearn on the next pick
	}
	if m != nil {
		s.m = m
	}
}

// Get returns the strategy recorded for a network and whether there is one.
func (s *fileNetStrategies) Get(network string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name, ok := s.m[network]
	return name, ok
}

// Set records the strategy for a network and persists the whole map. A write
// error is swallowed: this is a cache, and a failed persist must never break a
// pick that has otherwise succeeded. The in-memory value is updated regardless,
// so the rest of this run still benefits.
func (s *fileNetStrategies) Set(network, strategy string) {
	if network == "" || strategy == "" {
		return
	}
	s.mu.Lock()
	if s.m[network] == strategy {
		s.mu.Unlock()
		return // unchanged; skip the disk write
	}
	s.m[network] = strategy
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
func (s *fileNetStrategies) save(snapshot map[string]string) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("control: encode network strategies: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, netStrategyFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("control: create network-strategy temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("control: write network-strategy temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("control: sync network-strategy temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("control: close network-strategy temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("control: replace network-strategy file: %w", err)
	}
	return nil
}

// compile-time assertions that both stores satisfy the interface the daemon
// holds.
var (
	_ netStrategyStore = (*memNetStrategies)(nil)
	_ netStrategyStore = (*fileNetStrategies)(nil)
)
