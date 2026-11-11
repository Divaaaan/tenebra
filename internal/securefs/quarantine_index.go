package securefs

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"
)

const quarantineIndexLimit = 64 << 10
const quarantineReceiptLimit = 1024

type quarantineIndex struct {
	Version int      `json:"version"`
	IDs     []string `json:"ids"`
}

// The vault also contains deliberately untrusted original objects. Recovery
// discovery reads this single private, bounded index instead of recursively
// traversing those originals or treating them as trusted state.
func reserveQuarantineID(vault *Root, id string) error {
	index := quarantineIndex{Version: 1, IDs: []string{}}
	data, err := vault.ReadFile("quarantine-index.json", quarantineIndexLimit)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&index); err != nil {
			return ErrUntrusted
		}
		if decoder.Decode(new(any)) != io.EOF || index.Version != 1 || index.IDs == nil {
			return ErrUntrusted
		}
	}
	if len(index.IDs) >= quarantineReceiptLimit {
		return ErrLimit
	}
	seen := make(map[string]bool, len(index.IDs)+1)
	for _, existing := range append(index.IDs, id) {
		if len(existing) != 34 || !strings.HasPrefix(existing, "q_") || seen[existing] {
			return ErrUntrusted
		}
		if decoded, err := hex.DecodeString(existing[2:]); err != nil || len(decoded) != 16 || strings.ToLower(existing) != existing {
			return ErrUntrusted
		}
		seen[existing] = true
	}
	index.IDs = append(index.IDs, id)
	data, err = json.Marshal(index)
	if err != nil {
		return err
	}
	if len(data) > quarantineIndexLimit {
		return ErrLimit
	}
	state, err := vault.ReplaceFile("quarantine-index.json", data)
	if err != nil {
		return err
	}
	if state != Committed {
		return ErrChanged
	}
	return nil
}
