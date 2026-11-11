package installtx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func walkDeb(ctx context.Context, input io.Reader, budget *archiveBudget, data func(string, io.Reader) error) error {
	reader := contextReader{ctx: ctx, Reader: input}
	magic := make([]byte, 8)
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != "!<arch>\n" {
		return errors.Join(ErrProtocol, err)
	}
	header := make([]byte, 60)
	seen := map[string]bool{}
	for {
		n, err := io.ReadFull(reader, header)
		if n == 0 && errors.Is(err, io.EOF) {
			if !seen["debian-binary"] || !seen["control"] || !seen["data"] {
				return ErrProtocol
			}
			return nil
		}
		if err != nil || string(header[58:]) != "\x60\n" {
			return errors.Join(ErrProtocol, err)
		}
		budget.entries++
		if budget.entries > MaxArchiveEntries {
			return securefs.ErrLimit
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(header[:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if err != nil || size < 0 || size > MaxContainerBytes {
			return ErrProtocol
		}
		section := &io.LimitedReader{R: reader, N: size}
		switch {
		case name == "debian-binary":
			if seen[name] || len(seen) != 0 || size != 4 {
				return ErrProtocol
			}
			body, err := io.ReadAll(section)
			if err != nil || !bytes.Equal(body, []byte("2.0\n")) {
				return errors.Join(ErrProtocol, err)
			}
			seen[name] = true
		case oneOf(name, "control.tar", "control.tar.gz", "control.tar.xz", "control.tar.zst"):
			if !seen["debian-binary"] || seen["control"] {
				return ErrProtocol
			}
			seen["control"] = true
		case oneOf(name, "data.tar", "data.tar.gz"):
			if !seen["control"] || seen["data"] {
				return ErrProtocol
			}
			seen["data"] = true
			if err := data(name, section); err != nil {
				return err
			}
		case oneOf(name, "data.tar.xz", "data.tar.zst"):
			// Additional codecs require a pinned decoder and their own bounded
			// tests; never shell out to a path from the untrusted package.
			return securefs.ErrUnsupported
		default:
			return ErrProtocol
		}
		if _, err := io.Copy(io.Discard, section); err != nil {
			return err
		}
		if section.N != 0 {
			return io.ErrUnexpectedEOF
		}
		if size%2 != 0 {
			var pad [1]byte
			if _, err := io.ReadFull(reader, pad[:]); err != nil || pad[0] != '\n' {
				return errors.Join(ErrProtocol, err)
			}
		}
	}
}
