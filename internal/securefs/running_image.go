package securefs

import (
	"context"
	"encoding/hex"
)

type runningImageOps interface{ verifyRunningImage(handle) error }

// HashRunningExecutable binds a root-owned fixed role to THIS process's actual
// running kernel image. Equal bytes at a different inode do not establish it.
// Unsupported platforms refuse; there is no pathname-only identity fallback.
func (r *Root) HashRunningExecutable(ctx context.Context, name string, maxBytes int64) (string, error) {
	if r == nil || r.state == nil {
		return "", ErrClosed
	}
	parts, err := relativeParts(name)
	if err != nil || len(parts) != 1 || hasSealComponent(parts) {
		return "", ErrUntrusted
	}
	if maxBytes <= 0 || maxBytes > 64<<20 {
		return "", ErrLimit
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.actor != "uid:0" || s.policy.Mode != MachinePrivate && (s.policy.Mode != OperatorInput || s.policy.Principal != "uid:0") {
		return "", ErrUnsupported
	}
	ops, ok := s.ops.(runningImageOps)
	if !ok {
		return "", ErrUnsupported
	}
	if err := s.ready(false); err != nil {
		return "", err
	}
	h, _, err := s.open(parts, openRequest{})
	if err != nil {
		return "", err
	}
	defer s.ops.close(h)
	before, err := s.ops.inspect(h)
	if err != nil {
		return "", err
	}
	if before.kind != kindRegular || before.size <= 0 || before.size > maxBytes {
		return "", ErrLimit
	}
	if err := ops.verifyRunningImage(h); err != nil {
		return "", err
	}
	digest, count, err := readStreamHash(ctx, s.ops, h, maxBytes)
	if err != nil {
		return "", err
	}
	after, err := s.ops.inspect(h)
	if err != nil {
		return "", err
	}
	if count != before.size || before.id != after.id || before.change != after.change || before.size != after.size {
		return "", ErrChanged
	}
	current, _, err := s.open(parts, openRequest{})
	if err != nil {
		return "", err
	}
	info, err := s.ops.inspect(current)
	s.ops.close(current)
	if err != nil {
		return "", err
	}
	if info.id != before.id || info.change != before.change || info.size != before.size {
		return "", ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}
