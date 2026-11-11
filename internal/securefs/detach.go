package securefs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"
)

// Detach is for checkpoint-confirmed absence under the installer's verified
// pre-operational fence. It retains trusted bytes; it never quarantines,
// acknowledges an untrusted object, or grants a runtime capability.
type detachRecord struct {
	Version        int    `json:"version"`
	ID             string `json:"id"`
	State          string `json:"state"`
	SourceName     string `json:"source_name"`
	SourceObjectID string `json:"source_object_id"`
	SHA256         string `json:"sha256"`
	Bytes          int64  `json:"bytes"`
	Archive        string `json:"archive"`
}
type detachLocator struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
}

func detachSHA(data []byte) string        { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func detachLocatorName(rel string) string { return ".detach-index-" + detachSHA([]byte(rel)) + ".json" }
func detachRecordName(id string) string   { return ".detach-" + id + ".json" }
func validDetachID(id string) bool {
	if len(id) != 35 || !strings.HasPrefix(id, "d1_") {
		return false
	}
	value, err := hex.DecodeString(id[3:])
	return err == nil && len(value) == 16 && strings.ToLower(id) == id
}
func validDetachSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
func strictDetachJSON(data []byte, result any, fields ...string) error {
	if !utf8.Valid(data) {
		return ErrUntrusted
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return ErrUntrusted
	}
	allowed := map[string]bool{}
	for _, field := range fields {
		allowed[field] = true
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return ErrUntrusted
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || seen[key] {
			return ErrUntrusted
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrUntrusted
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != len(allowed) {
		return ErrUntrusted
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrUntrusted
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(result) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrUntrusted
	}
	return nil
}
func (s *rootState) readDetachFileLocked(name string, max int64) ([]byte, error) {
	h, parents, err := s.open([]string{name}, openRequest{})
	if err != nil {
		return nil, err
	}
	defer s.ops.close(h)
	defer func() {
		for _, parent := range parents {
			s.ops.close(parent)
		}
	}()
	return readBounded(s.ops, h, max)
}
func (s *rootState) detachRecordLocked(rel string) (detachRecord, error) {
	data, err := s.readDetachFileLocked(detachLocatorName(rel), 16<<10)
	if err != nil {
		return detachRecord{}, err
	}
	var locator detachLocator
	if strictDetachJSON(data, &locator, "version", "id") != nil || locator.Version != 1 || !validDetachID(locator.ID) {
		return detachRecord{}, ErrUntrusted
	}
	data, err = s.readDetachFileLocked(detachRecordName(locator.ID), 16<<10)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = ErrUntrusted
		}
		return detachRecord{}, err
	}
	var record detachRecord
	if strictDetachJSON(data, &record, "version", "id", "state", "source_name", "source_object_id", "sha256", "bytes", "archive") != nil || record.Version != 1 || record.ID != locator.ID || record.SourceName != rel || record.SourceObjectID == "" || !validDetachSHA(record.SHA256) || record.Bytes < 0 || record.Archive != ".detached-"+record.ID+".payload" || (record.State != "prepared" && record.State != "committed") {
		return detachRecord{}, ErrUntrusted
	}
	return record, nil
}
func (s *rootState) writeDetachRecordLocked(record detachRecord) (CommitState, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return Unchanged, err
	}
	return s.replaceFileLocked(detachRecordName(record.ID), data)
}
func (s *rootState) verifyDetachedArchiveLocked(record detachRecord, maxBytes int64) error {
	if record.Bytes > maxBytes {
		return ErrLimit
	}
	h, _, err := s.open([]string{record.Archive}, openRequest{})
	if err != nil {
		return err
	}
	defer s.ops.close(h)
	info, err := s.ops.inspect(h)
	if err != nil {
		return err
	}
	if info.id != record.SourceObjectID || info.size != record.Bytes {
		return ErrChanged
	}
	data, err := readBounded(s.ops, h, maxBytes)
	if err != nil {
		return err
	}
	if int64(len(data)) != record.Bytes || detachSHA(data) != record.SHA256 {
		return ErrChanged
	}
	return nil
}

func (r *Root) DetachFile(rel, expectedSHA256 string, maxBytes int64) (Receipt, CommitState, error) {
	if r == nil || r.state == nil {
		return Receipt{}, Unchanged, ErrClosed
	}
	parts, err := relativeParts(rel)
	if err != nil {
		return Receipt{}, Unchanged, err
	}
	if len(parts) != 1 || strings.HasPrefix(parts[0], ".") {
		return Receipt{}, Unchanged, ErrUnsupported
	}
	if maxBytes < 0 || !validDetachSHA(expectedSHA256) {
		return Receipt{}, Unchanged, ErrLimit
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(true); err != nil {
		return Receipt{}, Unchanged, err
	}
	if s.policy.Mode != MachinePrivate && s.policy.Mode != SameAccountPrivate {
		return Receipt{}, Unchanged, ErrUnsupported
	}
	if s.canonicalWriters != 1 || s.children != 0 {
		return Receipt{}, Unchanged, ErrBusy
	}
	record, recordErr := s.detachRecordLocked(rel)
	if recordErr != nil && !errors.Is(recordErr, fs.ErrNotExist) {
		return Receipt{}, Unchanged, recordErr
	}
	source, _, openErr := s.open(parts, openRequest{delete: true})
	if errors.Is(openErr, fs.ErrNotExist) {
		if recordErr != nil {
			return Receipt{}, Unchanged, fs.ErrNotExist
		}
		receipt := Receipt{ID: record.ID}
		if record.SHA256 != expectedSHA256 {
			return receipt, Indeterminate, ErrChanged
		}
		if err := s.verifyDetachedArchiveLocked(record, maxBytes); err != nil {
			return receipt, Indeterminate, err
		}
		if record.State != "committed" {
			// Observed absence and intact bytes cannot prove the earlier final
			// durability barrier. The installer keeps its fence for inspection.
			return receipt, Indeterminate, ErrChanged
		}
		return receipt, Committed, nil
	}
	if openErr != nil {
		return Receipt{}, Unchanged, openErr
	}
	defer s.ops.close(source)
	info, err := s.ops.inspect(source)
	if err != nil {
		return Receipt{}, Unchanged, err
	}
	data, err := readBounded(s.ops, source, maxBytes)
	if err != nil {
		return Receipt{}, Unchanged, err
	}
	if detachSHA(data) != expectedSHA256 {
		return Receipt{}, Unchanged, ErrChanged
	}
	if recordErr == nil && record.State == "prepared" {
		if record.SourceObjectID != info.id || record.SHA256 != expectedSHA256 || record.Bytes != int64(len(data)) {
			return Receipt{ID: record.ID}, Indeterminate, ErrChanged
		}
	} else {
		if recordErr == nil && record.SourceObjectID == info.id {
			return Receipt{ID: record.ID}, Indeterminate, ErrChanged
		}
		id, err := privateName("d1_")
		if err != nil {
			return Receipt{}, Unchanged, err
		}
		record = detachRecord{Version: 1, ID: id, State: "prepared", SourceName: rel, SourceObjectID: info.id, SHA256: expectedSHA256, Bytes: int64(len(data)), Archive: ".detached-" + id + ".payload"}
	}
	receipt := Receipt{ID: record.ID}
	if _, err := s.readDetachFileLocked(record.Archive, 1); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = ErrChanged
		}
		return receipt, Unchanged, err
	}
	if outcome, err := s.writeDetachRecordLocked(record); outcome != Committed || err != nil {
		if err == nil {
			err = ErrChanged
		}
		return receipt, Unchanged, err
	}
	locator, _ := json.Marshal(detachLocator{Version: 1, ID: record.ID})
	if outcome, err := s.replaceFileLocked(detachLocatorName(rel), locator); outcome != Committed || err != nil {
		if err == nil {
			err = ErrChanged
		}
		return receipt, Unchanged, err
	}
	outcome, err := s.ops.replacePrivate(s.h, source, rel, record.Archive)
	if outcome != Committed || err != nil {
		if err == nil {
			err = ErrChanged
		}
		if outcome == Committed {
			outcome = Indeterminate
		}
		return receipt, outcome, err
	}
	if _, err := s.readDetachFileLocked(rel, 1); !errors.Is(err, fs.ErrNotExist) {
		return receipt, Indeterminate, ErrChanged
	}
	if err := s.verifyDetachedArchiveLocked(record, maxBytes); err != nil {
		return receipt, Indeterminate, err
	}
	record.State = "committed"
	if outcome, err := s.writeDetachRecordLocked(record); outcome != Committed || err != nil {
		if err == nil {
			err = ErrChanged
		}
		return receipt, Indeterminate, err
	}
	return receipt, Committed, nil
}
