package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sync"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodeidentity"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

type repositoryFiles interface {
	ReadLegacy(context.Context) (LegacyBundle, error)
	Backup(context.Context, LegacyBundle) error
	ReplaceCanonical(context.Context, []byte) (securefs.CommitState, error)
}

type Store struct {
	mu                                  sync.RWMutex
	files                               repositoryFiles
	writer                              io.Closer
	directory                           string
	snapshot                            Snapshot
	gate                                chan struct{}
	admitted, poisoned, closing, closed bool
	closeDone                           chan struct{}
	closeError                          error
	fences                              uint64
}

func openWithFiles(ctx context.Context, dataDir string, files repositoryFiles, writer io.Closer) (*Store, error) {
	return openWithAllocator(ctx, dataDir, files, writer, nodeidentity.NewID)
}
func openWithAllocator(ctx context.Context, dataDir string, files repositoryFiles, writer io.Closer, nextID func() (string, error)) (_ *Store, failure error) {
	success := false
	defer func() {
		if !success && writer != nil {
			failure = errors.Join(failure, writer.Close())
		}
	}()
	if files == nil || writer == nil {
		return nil, ErrCorruptState
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bundle, err := files.ReadLegacy(ctx)
	if err != nil {
		return nil, err
	}
	var snapshot Snapshot
	data := bytes.TrimSpace(bundle.Profiles.Bytes)
	if bundle.Profiles.Present && len(data) > 0 && data[0] == '{' {
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, err
		}
		if err := snapshot.Validate(); err != nil {
			return nil, err
		}
	} else {
		// Back up the exact observed source, including malformed input, before
		// attempting migration. Never back up a fabricated default.
		if err := files.Backup(ctx, cloneLegacyFiles(bundle)); err != nil {
			return nil, err
		}
		snapshot, err = MigrateLegacy(bundle, nextID)
		if err != nil {
			return nil, err
		}
		data, err = json.Marshal(snapshot)
		if err != nil {
			return nil, ErrCorruptState
		}
		if len(data) > canonicalFileLimit {
			return nil, securefs.ErrLimit
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state, err := files.ReplaceCanonical(ctx, data)
		if state != securefs.Committed || err != nil {
			if state != securefs.Unchanged {
				return nil, errors.Join(ErrDurabilityUnknown, err)
			}
			if err == nil {
				err = ErrCorruptState
			}
			return nil, err
		}
	}
	store := &Store{files: files, writer: writer, directory: dataDir, snapshot: snapshot, gate: make(chan struct{}, 1), closeDone: make(chan struct{})}
	store.gate <- struct{}{}
	success = true
	return store, nil
}

const canonicalFileLimit = 64 << 20

func cloneLegacyFiles(bundle LegacyBundle) LegacyBundle {
	clone := func(file LegacyFile) LegacyFile { return LegacyFile{file.Present, bytes.Clone(file.Bytes)} }
	return LegacyBundle{clone(bundle.Profiles), clone(bundle.Settings), clone(bundle.LastGood)}
}

func (s *Store) readinessLocked(requireAdmission bool) error {
	if s.closed || s.closing {
		return ErrClosed
	}
	if s.poisoned {
		return ErrDurabilityUnknown
	}
	if requireAdmission && !s.admitted {
		return ErrMaintenancePending
	}
	return nil
}
func (s *Store) View() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot, err := cloneSnapshot(s.snapshot)
	if err != nil {
		return Snapshot{}
	}
	return snapshot
}
func (s *Store) Ready() error { s.mu.RLock(); defer s.mu.RUnlock(); return s.readinessLocked(true) }

func (s *Store) Transact(ctx context.Context, revision uint64, mutate func(*Snapshot) error) (Snapshot, error) {
	if mutate == nil {
		return Snapshot{}, ErrCorruptState
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.RLock()
	ready := s.readinessLocked(true)
	s.mu.RUnlock()
	if ready != nil {
		return Snapshot{}, ready
	}
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	s.mu.RLock()
	ready = s.readinessLocked(true)
	before, err := cloneSnapshot(s.snapshot)
	s.mu.RUnlock()
	if ready != nil {
		return Snapshot{}, ready
	}
	if err != nil {
		return Snapshot{}, err
	}
	if revision != AnyRevision && revision != before.Revision {
		return Snapshot{}, ErrRevisionConflict
	}
	working, err := cloneSnapshot(before)
	if err != nil {
		return Snapshot{}, err
	}
	if err := mutate(&working); err != nil {
		return Snapshot{}, err
	}
	// Sever every pointer retained by the callback before validation or IO.
	candidate, err := cloneSnapshot(working)
	if err != nil {
		return Snapshot{}, err
	}
	if candidate.SchemaVersion != before.SchemaVersion || candidate.IdentityVersion != before.IdentityVersion || candidate.MatcherVersion != before.MatcherVersion || candidate.MigrationID != before.MigrationID {
		return Snapshot{}, ErrCorruptState
	}
	candidate.Revision = before.Revision
	for id, p := range candidate.Profiles {
		if old, exists := before.Profiles[id]; exists {
			p.Revision = old.Revision
		} else {
			p.Revision = 0
		}
		candidate.Profiles[id] = p
	}
	if equalJSON(before, candidate) {
		return cloneSnapshot(before)
	}
	if before.Revision == math.MaxUint64 {
		return Snapshot{}, ErrCorruptState
	}
	for id, p := range candidate.Profiles {
		old, exists := before.Profiles[id]
		if !exists {
			p.Revision = 1
		} else if !equalJSON(old, p) {
			if old.Revision == math.MaxUint64 {
				return Snapshot{}, ErrCorruptState
			}
			p.Revision = old.Revision + 1
		}
		candidate.Profiles[id] = p
	}
	candidate.Revision = before.Revision + 1
	if err := candidate.Validate(); err != nil {
		return Snapshot{}, err
	}
	if err := validateSnapshotTransition(before, candidate); err != nil {
		return Snapshot{}, err
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if len(data) > canonicalFileLimit {
		return Snapshot{}, securefs.ErrLimit
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	state, err := s.files.ReplaceCanonical(ctx, data)
	if state != securefs.Committed || err != nil {
		if state != securefs.Unchanged {
			s.mu.Lock()
			s.poisoned = true
			s.admitted = false
			s.mu.Unlock()
			return Snapshot{}, errors.Join(ErrDurabilityUnknown, err)
		}
		if err == nil {
			err = ErrCorruptState
		}
		return Snapshot{}, err
	}
	// A confirmed durable commit remains committed even if cancellation/fencing
	// arrived during fsync. Never report cancellation and hide its outcome.
	s.mu.Lock()
	s.snapshot = candidate
	s.mu.Unlock()
	return cloneSnapshot(candidate)
}
func (s *Store) Admit() error {
	select {
	case <-s.gate:
	default:
		return ErrMaintenancePending
	}
	defer func() { s.gate <- struct{}{} }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.readinessLocked(false); err != nil {
		return err
	}
	if s.fences != 0 {
		return ErrMaintenancePending
	}
	s.admitted = true
	return nil
}
func (s *Store) Fence(ctx context.Context) error {
	s.mu.Lock()
	s.admitted = false
	ready := s.readinessLocked(false)
	if ready == nil {
		s.fences++
	}
	s.mu.Unlock()
	if ready != nil {
		return ready
	}
	defer func() { s.mu.Lock(); s.fences--; s.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.gate:
		defer func() { s.gate <- struct{}{} }()
		// The drained transaction may have discovered an uncertain final sync.
		// Holding the gate does not itself acknowledge durable state.
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.readinessLocked(false)
	}
}
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed || s.closing {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.closeError
	}
	s.admitted = false
	s.closing = true
	s.mu.Unlock()
	<-s.gate
	err := s.writer.Close()
	s.mu.Lock()
	s.closed = true
	s.closeError = err
	close(s.closeDone)
	s.mu.Unlock()
	s.gate <- struct{}{}
	return err
}

func validateSnapshotTransition(before, candidate Snapshot) error {
	previous := map[string]model.Node{}
	for _, p := range before.Profiles {
		for _, server := range p.Servers {
			previous[server.ID] = server.Node
		}
	}
	for _, p := range candidate.Profiles {
		for _, server := range p.Servers {
			old, exists := previous[server.ID]
			if !exists {
				continue
			}
			matcher, err := nodeidentity.NewMatcher([]model.Node{old})
			if err != nil {
				return err
			}
			matches, err := matcher.Candidates(server.Node)
			if err != nil {
				return err
			}
			if len(matches) != 1 {
				return profile.ErrIdentityRebind
			}
		}
	}
	return nil
}
