package installtx

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type archiveBudget struct {
	expanded int64
	entries  int
}
type expandedReader struct {
	ctx context.Context
	io.Reader
	budget *archiveBudget
}

func (r *expandedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	remaining := MaxExpandedBytes - r.budget.expanded
	if remaining < 0 {
		return 0, securefs.ErrLimit
	}
	if int64(len(p)) > remaining+1 {
		p = p[:remaining+1]
	}
	n, err := r.Reader.Read(p)
	if n < 0 || n > len(p) {
		return 0, ErrProtocol
	}
	// A nested gzip reader can consume bytes from the outer tar through the
	// same budget during Read. Check the balance after both reads.
	if int64(n) > MaxExpandedBytes-r.budget.expanded {
		return 0, securefs.ErrLimit
	}
	r.budget.expanded += int64(n)
	return n, err
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func archiveName(name string, directory bool) (string, error) {
	if !utf8.ValidString(name) || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return "", ErrProtocol
	}
	if directory && (name == "." || name == "./") {
		return "", nil
	}
	name = strings.TrimPrefix(name, "./")
	if directory {
		name = strings.TrimSuffix(name, "/")
	}
	parts := strings.Split(name, "/")
	if len(parts) > 32 {
		return "", securefs.ErrLimit
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", ErrProtocol
		}
	}
	return name, nil
}
func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

// Walk physical tar headers rather than archive/tar.Next over a whole stream:
// Next hides PAX/GNU metadata records, which otherwise evade an entry budget.
// Only regular USTAR/GNU headers and directories are currently accepted.
// Extended metadata/sparse/link/device records fail closed before allocation.
func walkTar(ctx context.Context, input io.Reader, budget *archiveBudget, visit func(*tar.Header, io.Reader) error) error {
	reader := &expandedReader{ctx: ctx, Reader: input, budget: budget}
	header := make([]byte, 512)
	seen := map[string]bool{}
	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			return errors.Join(ErrProtocol, err)
		}
		if allZero(header) {
			if _, err := io.ReadFull(reader, header); err != nil || !allZero(header) {
				return errors.Join(ErrProtocol, err)
			}
			buffer := make([]byte, 32<<10)
			for {
				n, err := reader.Read(buffer)
				if !allZero(buffer[:n]) {
					return ErrProtocol
				}
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
				if n == 0 {
					return io.ErrNoProgress
				}
			}
		}
		budget.entries++
		if budget.entries > MaxArchiveEntries {
			return securefs.ErrLimit
		}
		if !oneOf(string(header[156]), string(tar.TypeReg), string(tar.TypeRegA), string(tar.TypeDir)) {
			return securefs.ErrUnsupported
		}
		// The standard parser verifies checksums and interprets the fixed header;
		// it cannot consume hidden metadata because the type was checked above.
		entry, err := tar.NewReader(bytes.NewReader(header)).Next()
		if err != nil {
			return errors.Join(ErrProtocol, err)
		}
		directory := entry.Typeflag == tar.TypeDir
		name, err := archiveName(entry.Name, directory)
		if err != nil || entry.Size < 0 || entry.Size > MaxExpandedBytes-budget.expanded || entry.Mode&06000 != 0 || entry.Linkname != "" {
			return errors.Join(ErrProtocol, err)
		}
		entry.Name = name
		if directory {
			if entry.Size != 0 {
				return ErrProtocol
			}
			continue
		}
		if seen[name] {
			return ErrProtocol
		}
		seen[name] = true
		section := &io.LimitedReader{R: reader, N: entry.Size}
		if err := visit(entry, section); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, section); err != nil {
			return err
		}
		if section.N != 0 {
			return io.ErrUnexpectedEOF
		}
		padding := (512 - entry.Size%512) % 512
		if padding > 0 {
			if _, err := io.ReadFull(reader, header[:padding]); err != nil || !allZero(header[:padding]) {
				return errors.Join(ErrProtocol, err)
			}
		}
	}
}
