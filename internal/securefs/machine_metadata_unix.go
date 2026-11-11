//go:build linux || darwin

package securefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"strings"
	"unicode/utf8"
)

const machineMetadataLimit = 64 << 10

type privateMetadataOps interface {
	verifyPrivateMetadata(handle) error
}

// ReadMachinePrivateMetadata reads a root-owned 0600 regular file through its
// retained machine-directory handle. Metadata permission and identity checks
// cover the same opened file before and after the bounded read.
func (r *Root) ReadMachinePrivateMetadata(ctx context.Context, name string) ([]byte, error) {
	if r == nil || r.state == nil {
		return nil, ErrClosed
	}
	parts, err := relativeParts(name)
	if err != nil || len(parts) != 1 || hasSealComponent(parts) {
		return nil, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.policy.Mode != MachineContainer || s.actor != "uid:0" || s.poisoned {
		return nil, ErrUnsupported
	}
	ops, ok := s.ops.(privateMetadataOps)
	if !ok {
		return nil, ErrUnsupported
	}
	parent, err := s.ops.inspect(s.h)
	if err != nil {
		return nil, err
	}
	if parent.kind != kindDirectory || verifyObject(parent, Policy{Mode: OperatorInput, Principal: "uid:0"}, s.actor, false) != nil {
		return nil, ErrUntrusted
	}
	h, _, err := machineMetadataOpen(s, parts[0], Policy{Mode: MachinePrivate})
	if err != nil {
		return nil, err
	}
	defer s.ops.close(h)
	body, err := readBoundedChecked(s.ops, h, machineMetadataLimit, func(info objectInfo) error {
		if err := verifyObject(info, Policy{Mode: MachinePrivate}, s.actor, false); err != nil {
			return err
		}
		return ops.verifyPrivateMetadata(h)
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return body, nil
}

// PublishMachineMetadata is a bounded root-only publication into a retained
// root-owned, non-writable machine namespace. Empty previousSHA is CREATE ONLY.
// It grants neither executable publication nor general MachineContainer writes.
func (r *Root) PublishMachineMetadata(ctx context.Context, name string, body []byte, previousSHA string, mode uint32) (state CommitState, result error) {
	if r == nil || r.state == nil {
		return Unchanged, ErrClosed
	}
	parts, err := relativeParts(name)
	if err != nil || len(parts) != 1 || len(name) > 128 || strings.HasPrefix(name, ".") || len(body) == 0 ||
		!utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 || mode != 0600 && mode != 0644 {
		return Unchanged, ErrUnsupported
	}
	if len(body) > machineMetadataLimit {
		return Unchanged, ErrLimit
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return Unchanged, ErrUnsupported
		}
	}
	if previousSHA != "" {
		decoded, err := hex.DecodeString(previousSHA)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != previousSHA {
			return Unchanged, ErrUnsupported
		}
	}
	if err := ctx.Err(); err != nil {
		return Unchanged, err
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Unchanged, ErrClosed
	}
	if s.policy.Mode != MachineContainer || s.actor != "uid:0" || s.readOnlyBinding || s.frozen || s.poisoned {
		return Unchanged, ErrUnsupported
	}
	if s.leases != 0 || s.publishing {
		return Unchanged, ErrBusy
	}
	parent, err := s.ops.inspect(s.h)
	if err != nil {
		return Unchanged, err
	}
	// Unlike container traversal this rejects EVERY untrusted writer/creator,
	// including sticky namespaces. Public read alone does not weaken ownership.
	policy := Policy{Mode: OperatorInput, Principal: "uid:0"}
	if parent.kind != kindDirectory || verifyObject(parent, policy, s.actor, false) != nil {
		return Unchanged, ErrUntrusted
	}
	lockName := ".tenebra-machine-metadata.lock"
	lock, err := s.ops.openAt(s.h, lockName, openRequest{write: true})
	if errors.Is(err, fs.ErrNotExist) {
		lock, err = s.ops.openAt(s.h, lockName, openRequest{write: true, create: true, policy: Policy{Mode: MachinePrivate}, permissions: 0600})
		if errors.Is(err, fs.ErrExist) {
			lock, err = s.ops.openAt(s.h, lockName, openRequest{write: true})
		}
	}
	if err != nil {
		return Unchanged, err
	}
	defer func() { result = errors.Join(result, s.ops.close(lock)) }()
	lockInfo, err := s.ops.inspect(lock)
	if err != nil {
		return Unchanged, err
	}
	if lockInfo.kind != kindRegular || verifyObject(lockInfo, Policy{Mode: MachinePrivate}, s.actor, false) != nil {
		return Unchanged, ErrUntrusted
	}
	if err := s.ops.lock(lock); err != nil {
		return Unchanged, err
	}
	defer func() { result = errors.Join(result, s.ops.unlock(lock)) }()
	old, oldInfo, err := machineMetadataOpen(s, name, policy)
	if err == nil {
		oldBody, readErr := readBounded(s.ops, old, machineMetadataLimit)
		s.ops.close(old)
		if readErr != nil {
			return Unchanged, readErr
		}
		sum := sha256.Sum256(oldBody)
		if previousSHA == "" || hex.EncodeToString(sum[:]) != previousSHA {
			return Unchanged, ErrChanged
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Unchanged, err
	} else if previousSHA != "" {
		return Unchanged, ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return Unchanged, err
	}
	temporary, err := privateName(".tenebra-meta-")
	if err != nil {
		return Unchanged, err
	}
	temp, err := s.ops.openAt(s.h, temporary, openRequest{create: true, write: true, policy: s.policy, permissions: mode, machineMetadata: true})
	if err != nil {
		return Unchanged, err
	}
	defer func() {
		if state == Unchanged {
			_ = s.ops.removeOwned(temp, s.h, temporary)
		}
		result = errors.Join(result, s.ops.close(temp))
	}()
	info, err := s.ops.inspect(temp)
	if err != nil {
		return Unchanged, err
	}
	if info.kind != kindRegular || verifyObject(info, policy, s.actor, false) != nil {
		return Unchanged, ErrUntrusted
	}
	expected, size, err := writeStream(ctx, s.ops, temp, bytes.NewReader(body), machineMetadataLimit)
	if err != nil {
		return Unchanged, err
	}
	if err := s.ops.flush(temp); err != nil {
		return Unchanged, err
	}
	check, observed, err := machineMetadataOpen(s, temporary, policy)
	if err != nil {
		return Unchanged, err
	}
	digest, count, readErr := readStreamHash(ctx, s.ops, check, size)
	s.ops.close(check)
	if readErr != nil {
		return Unchanged, readErr
	}
	if observed.id != info.id || digest != expected || count != size {
		return Unchanged, ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return Unchanged, err
	}
	if previousSHA == "" {
		err = s.ops.rename(temp, s.h, name, false)
		if err != nil {
			// rename includes parent fsync; a failure after the rename cannot
			// be reported as unchanged. Inspect the SAME retained namespace.
			published, found, checkErr := machineMetadataOpen(s, name, policy)
			if checkErr == nil {
				s.ops.close(published)
			}
			if checkErr == nil && found.id == info.id {
				return Indeterminate, err
			}
			remaining, still, retainedErr := machineMetadataOpen(s, temporary, policy)
			if retainedErr == nil {
				s.ops.close(remaining)
			}
			if retainedErr == nil && still.id == info.id {
				return Unchanged, err
			}
			return Indeterminate, err
		}
		state = Committed
	} else {
		// The short cross-process metadata lock excludes cooperating writers;
		// recheck exact old identity immediately before replacement as well.
		prior, found, err := machineMetadataOpen(s, name, policy)
		if err != nil {
			return Unchanged, err
		}
		s.ops.close(prior)
		if found.id != oldInfo.id || found.change != oldInfo.change {
			return Unchanged, ErrChanged
		}
		state, err = s.ops.replacePrivate(s.h, temp, temporary, name)
		if err != nil || state != Committed {
			return state, errors.Join(err, ErrChanged)
		}
	}
	canonical, found, err := machineMetadataOpen(s, name, policy)
	if err != nil {
		return Indeterminate, err
	}
	digest, count, readErr = readStreamHash(ctx, s.ops, canonical, size)
	s.ops.close(canonical)
	if readErr != nil {
		return Indeterminate, readErr
	}
	if found.id != info.id || digest != expected || count != size {
		return Indeterminate, ErrChanged
	}
	s.inventory.record(s.prefix+name, info.id)
	return Committed, nil
}

func machineMetadataOpen(s *rootState, name string, policy Policy) (handle, objectInfo, error) {
	h, err := s.ops.openAt(s.h, name, openRequest{})
	if err != nil {
		return 0, objectInfo{}, err
	}
	info, err := s.ops.inspect(h)
	if err == nil && (info.kind != kindRegular || verifyObject(info, policy, s.actor, false) != nil) {
		err = ErrUntrusted
	}
	if err != nil {
		s.ops.close(h)
		return 0, info, err
	}
	return h, info, nil
}
