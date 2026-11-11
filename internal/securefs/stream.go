package securefs

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
)

// ReplaceStream is the bounded counterpart of ReplaceFile. Only private 0600
// metadata/resources or 0700 executables can be exclusively created. It shares
// ReplaceFile's durable replacement engine and never chmods a source/old target.
func (r *Root) ReplaceStream(ctx context.Context, rel string, input io.Reader, maxBytes int64, mode uint32) (CommitState, error) {
	if r == nil || r.state == nil {
		return Unchanged, ErrClosed
	}
	if input == nil || maxBytes < 0 || mode != 0600 && mode != 0700 {
		return Unchanged, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return Unchanged, err
	}
	parts, err := relativeParts(rel)
	if err != nil {
		return Unchanged, err
	}
	if hasSealComponent(parts) {
		return Unchanged, ErrUnsupported
	}
	if len(parts) > 1 {
		parent, err := r.OpenDir(strings.Join(parts[:len(parts)-1], "/"), r.state.policy, false)
		if err != nil {
			return Unchanged, err
		}
		defer parent.Close()
		return parent.ReplaceStream(ctx, parts[len(parts)-1], input, maxBytes, mode)
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceStreamLocked(ctx, parts[0], input, maxBytes, mode)
}
func writeStream(ctx context.Context, ops nativeOps, h handle, input io.Reader, maxBytes int64) ([32]byte, int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, total, err
		}
		n, readErr := input.Read(buffer)
		if n < 0 || n > len(buffer) {
			return [32]byte{}, total, ErrChanged
		}
		if int64(n) > maxBytes-total {
			return [32]byte{}, total, ErrLimit
		}
		for offset := 0; offset < n; {
			count, err := ops.write(h, buffer[offset:n])
			if err != nil {
				return [32]byte{}, total, err
			}
			if count <= 0 || count > n-offset {
				return [32]byte{}, total, io.ErrShortWrite
			}
			offset += count
		}
		hash.Write(buffer[:n])
		total += int64(n)
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return [32]byte{}, total, readErr
			}
			break
		}
		if n == 0 {
			return [32]byte{}, total, io.ErrNoProgress
		}
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, total, nil
}
func readStreamHash(ctx context.Context, ops nativeOps, h handle, maxBytes int64) ([32]byte, int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, total, err
		}
		n, err := ops.read(h, buffer)
		if n < 0 || n > len(buffer) {
			return [32]byte{}, total, ErrChanged
		}
		if int64(n) > maxBytes-total {
			return [32]byte{}, total, ErrLimit
		}
		hash.Write(buffer[:n])
		total += int64(n)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return [32]byte{}, total, err
			}
			break
		}
		if n == 0 {
			return [32]byte{}, total, io.ErrNoProgress
		}
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, total, nil
}
