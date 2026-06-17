package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// storeFile is the single file every profile is persisted to, inside the
// store's directory.
const storeFile = "profiles.json"

// ErrNotFound is returned when an operation targets a profile ID that is not in
// the store.
var ErrNotFound = errors.New("profile not found")

// ErrExists is returned by Add when a profile with the same ID is already
// stored.
var ErrExists = errors.New("profile already exists")

// Store persists profiles to a directory as one JSON file. It is safe for
// concurrent use.
type Store struct {
	dir string

	mu       sync.Mutex
	profiles []Profile
}

// Open binds a Store to dir, creating the directory if needed and loading any
// existing profiles.json. A missing file is treated as an empty store; a
// corrupt file is an error.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("profile: empty store directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	s := &Store{dir: dir}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads profiles.json into memory. Caller need not hold the lock; Open is
// the only caller and runs before the Store is shared.
func (s *Store) load() error {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		s.profiles = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("read profiles: %w", err)
	}
	if len(data) == 0 {
		s.profiles = nil
		return nil
	}
	var profiles []Profile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return fmt.Errorf("parse profiles: %w", err)
	}
	s.profiles = profiles
	return nil
}

// path is the absolute path to the backing file.
func (s *Store) path() string { return filepath.Join(s.dir, storeFile) }

// List returns a copy of all stored profiles. Mutating the result does not
// affect the store.
func (s *Store) List() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, len(s.profiles))
	copy(out, s.profiles)
	return out
}

// Get returns the profile with the given ID and whether it was found. The
// returned profile is a copy.
func (s *Store) Get(id string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(id)
	if i < 0 {
		return Profile{}, false
	}
	return s.profiles[i], true
}

// Add stores a new profile and persists. It fails if the ID already exists.
func (s *Store) Add(p Profile) error {
	if p.ID == "" {
		return errors.New("profile: empty id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexOf(p.ID) >= 0 {
		return fmt.Errorf("add %s: %w", p.ID, ErrExists)
	}
	s.profiles = append(s.profiles, p)
	if err := s.save(); err != nil {
		// Roll back the in-memory change so it stays consistent with disk.
		s.profiles = s.profiles[:len(s.profiles)-1]
		return err
	}
	return nil
}

// Update replaces an existing profile, matched by ID, and persists. It is the
// path used after a subscription refresh: the caller rebuilds Servers (whose
// stable IDs are preserved for unchanged servers) and writes the whole profile
// back.
func (s *Store) Update(p Profile) error {
	if p.ID == "" {
		return errors.New("profile: empty id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(p.ID)
	if i < 0 {
		return fmt.Errorf("update %s: %w", p.ID, ErrNotFound)
	}
	prev := s.profiles[i]
	s.profiles[i] = p
	if err := s.save(); err != nil {
		s.profiles[i] = prev
		return err
	}
	return nil
}

// Remove deletes the profile with the given ID and persists.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(id)
	if i < 0 {
		return fmt.Errorf("remove %s: %w", id, ErrNotFound)
	}
	removed := s.profiles[i]
	s.profiles = append(s.profiles[:i:i], s.profiles[i+1:]...)
	if err := s.save(); err != nil {
		// Restore the slice to its prior contents on failure.
		s.profiles = append(s.profiles, Profile{})
		copy(s.profiles[i+1:], s.profiles[i:])
		s.profiles[i] = removed
		return err
	}
	return nil
}

// indexOf returns the slice index of the profile with id, or -1. Callers hold
// the lock.
func (s *Store) indexOf(id string) int {
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			return i
		}
	}
	return -1
}

// save writes the in-memory profiles to disk atomically: it serialises to a
// temp file in the same directory, fsyncs it, then renames over the target so a
// reader never sees a half-written file. Callers hold the lock.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.profiles, "", "  ")
	if err != nil {
		return fmt.Errorf("encode profiles: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, storeFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// If anything below fails, don't leave the temp file behind.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, s.path()); err != nil {
		return fmt.Errorf("replace profiles file: %w", err)
	}
	return nil
}
