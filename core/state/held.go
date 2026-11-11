package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"sync"

	"github.com/Divaaaan/tenebra/core/nodeidentity"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

type CheckpointRef struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	SHA256   string `json:"sha256"`
}
type SchemaReceipt struct {
	MigrationID     string `json:"migration_id"`
	Schema          uint32 `json:"schema"`
	Revision        uint64 `json:"revision"`
	CanonicalSHA256 string `json:"canonical_sha256"`
}
type heldRecord struct {
	Version          int                      `json:"version"`
	TxID             string                   `json:"tx_id"`
	Directory        string                   `json:"directory"`
	Ref              CheckpointRef            `json:"ref"`
	Backup           string                   `json:"backup"`
	Before           SchemaReceipt            `json:"before"`
	Intended         *SchemaReceipt           `json:"intended,omitempty"`
	RestorePrepared  bool                     `json:"restore_prepared,omitempty"`
	RestoreCompleted bool                     `json:"restore_completed,omitempty"`
	RestoreDetaches  map[string]restoreDetach `json:"restore_detaches,omitempty"`
}
type restoreDetach struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
type HeldRepository struct {
	mu               sync.Mutex
	files            *rootRepositoryFiles
	writer           io.Closer
	directory        string
	closed, poisoned bool
}

func OpenHeld(ctx context.Context, data, backupRoot *securefs.Root) (*HeldRepository, error) {
	if data == nil || backupRoot == nil {
		return nil, securefs.ErrUntrusted
	}
	directory, err := data.Path()
	if err != nil {
		return nil, err
	}
	writer, err := data.LockWriter(ctx)
	if err != nil {
		return nil, err
	}
	return &HeldRepository{files: &rootRepositoryFiles{data: data, backup: backupRoot}, writer: writer, directory: directory}, nil
}
func validTransactionID(id string) bool {
	if id == "" || len(id) > 96 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}
func (h *HeldRepository) recordName(txID string) (string, error) {
	if !validTransactionID(txID) {
		return "", ErrCorruptState
	}
	return "held-" + txID + ".json", nil
}
func (h *HeldRepository) readRecord(txID string) (heldRecord, error) {
	name, err := h.recordName(txID)
	if err != nil {
		return heldRecord{}, err
	}
	data, err := h.files.backup.ReadFile(name, 64<<10)
	if err != nil {
		return heldRecord{}, err
	}
	if checkJSON(data) != nil || validateShape(data, reflect.TypeOf(heldRecord{}), 0) != nil {
		return heldRecord{}, ErrCorruptState
	}
	var record heldRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Version != 1 || record.TxID != txID || record.Directory != h.directory || record.Ref.ID == "" || !validChecksum(record.Ref.SHA256) || record.Backup != "legacy-"+record.Ref.SHA256 {
		return heldRecord{}, ErrCorruptState
	}
	return record, nil
}
func (h *HeldRepository) writeRecord(record heldRecord) error {
	name, err := h.recordName(record.TxID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return ErrCorruptState
	}
	state, err := h.files.backup.ReplaceFile(name, data)
	if state != securefs.Committed || err != nil {
		if state != securefs.Unchanged {
			h.poisoned = true
			return errors.Join(ErrDurabilityUnknown, err)
		}
		if err == nil {
			err = ErrCorruptState
		}
		return err
	}
	return nil
}
func checksum(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
func validChecksum(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
func inspectBundle(bundle LegacyBundle) (SchemaReceipt, error) {
	if !bundle.Profiles.Present {
		return SchemaReceipt{}, nil
	}
	data := bytes.TrimSpace(bundle.Profiles.Bytes)
	if len(data) == 0 || checkJSON(data) != nil {
		return SchemaReceipt{}, ErrCorruptState
	}
	receipt := SchemaReceipt{CanonicalSHA256: checksum(bundle.Profiles.Bytes)}
	if data[0] == '[' {
		return receipt, nil
	}
	if data[0] != '{' {
		return SchemaReceipt{}, ErrCorruptState
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return SchemaReceipt{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return SchemaReceipt{}, err
	}
	receipt.Schema = uint32(snapshot.SchemaVersion)
	receipt.Revision = snapshot.Revision
	receipt.MigrationID = snapshot.MigrationID
	return receipt, nil
}

func (h *HeldRepository) Checkpoint(ctx context.Context, txID string) (CheckpointRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return CheckpointRef{}, ErrClosed
	}
	if h.poisoned {
		return CheckpointRef{}, ErrDurabilityUnknown
	}
	if record, err := h.readRecord(txID); err == nil {
		if _, err := h.readCheckpoint(ctx, record); err != nil {
			return CheckpointRef{}, err
		}
		return record.Ref, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return CheckpointRef{}, err
	}
	bundle, err := h.files.readBundle(ctx, false)
	if err != nil {
		return CheckpointRef{}, err
	}
	before, err := inspectBundle(bundle)
	if err != nil {
		return CheckpointRef{}, err
	}
	if err := h.files.Backup(ctx, bundle); err != nil {
		return CheckpointRef{}, err
	}
	_, manifest, err := manifestFor(bundle)
	if err != nil {
		return CheckpointRef{}, err
	}
	id, err := nodeidentity.NewID()
	if err != nil {
		return CheckpointRef{}, err
	}
	record := heldRecord{Version: 1, TxID: txID, Directory: h.directory, Ref: CheckpointRef{ID: id, Revision: before.Revision, SHA256: checksum(manifest)}, Backup: backupName(manifest), Before: before}
	if err := h.writeRecord(record); err != nil {
		return CheckpointRef{}, err
	}
	return record.Ref, nil
}

func (h *HeldRepository) readCheckpoint(ctx context.Context, record heldRecord) (LegacyBundle, error) {
	manifestBytes, err := h.files.backup.ReadFile(record.Backup+"/manifest.json", 64<<10)
	if err != nil {
		return LegacyBundle{}, err
	}
	if checksum(manifestBytes) != record.Ref.SHA256 || backupName(manifestBytes) != record.Backup {
		return LegacyBundle{}, ErrCorruptState
	}
	var manifest backupManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if checkJSON(manifestBytes) != nil || validateShape(manifestBytes, reflect.TypeOf(backupManifest{}), 0) != nil || decoder.Decode(&manifest) != nil || decoder.Decode(new(any)) != io.EOF || manifest.Version != 1 || len(manifest.Files) != 3 {
		return LegacyBundle{}, ErrCorruptState
	}
	var bundle LegacyBundle
	for name, destination := range map[string]*LegacyFile{"profiles.json": &bundle.Profiles, "settings.json": &bundle.Settings, "lastgood.json": &bundle.LastGood} {
		if err := ctx.Err(); err != nil {
			return LegacyBundle{}, err
		}
		metadata, exists := manifest.Files[name]
		if !exists || metadata.Bytes < 0 || !validChecksum(metadata.SHA256) {
			return LegacyBundle{}, ErrCorruptState
		}
		if !metadata.Present {
			if metadata.Bytes != 0 || metadata.SHA256 != checksum(nil) {
				return LegacyBundle{}, ErrCorruptState
			}
			continue
		}
		limit := int64(canonicalFileLimit)
		if name == "settings.json" {
			limit = settingsFileLimit
		} else if name == "lastgood.json" {
			limit = hintsFileLimit
		}
		data, err := h.files.backup.ReadFile(record.Backup+"/"+name, limit)
		if err != nil {
			return LegacyBundle{}, err
		}
		if len(data) != metadata.Bytes || checksum(data) != metadata.SHA256 {
			return LegacyBundle{}, ErrCorruptState
		}
		*destination = LegacyFile{true, data}
	}
	return bundle, nil
}

// This borrowed closer never unlocks the kernel writer. The held owner retains
// that real lock while the exact normal repository engine performs migration.
type heldWriterBorrow struct{}

func (heldWriterBorrow) Close() error { return nil }

type heldMigrationFiles struct {
	repositoryFiles
	holder *HeldRepository
	record *heldRecord
}

func (f *heldMigrationFiles) ReplaceCanonical(ctx context.Context, data []byte) (securefs.CommitState, error) {
	receipt, err := inspectBundle(LegacyBundle{Profiles: LegacyFile{true, data}})
	if err != nil {
		return securefs.Unchanged, err
	}
	f.record.Intended = &receipt
	if err := f.holder.writeRecord(*f.record); err != nil {
		return securefs.Unchanged, err
	}
	return f.repositoryFiles.ReplaceCanonical(ctx, data)
}
func (h *HeldRepository) Migrate(ctx context.Context, txID string) (SchemaReceipt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return SchemaReceipt{}, ErrClosed
	}
	if h.poisoned {
		return SchemaReceipt{}, ErrDurabilityUnknown
	}
	record, err := h.readRecord(txID)
	if err != nil {
		return SchemaReceipt{}, err
	}
	if record.RestorePrepared {
		return SchemaReceipt{}, ErrMaintenancePending
	}
	checkpoint, err := h.readCheckpoint(ctx, record)
	if err != nil {
		return SchemaReceipt{}, err
	}
	current, err := h.files.ReadLegacy(ctx)
	if err != nil {
		return SchemaReceipt{}, err
	}
	observed, err := inspectBundle(current)
	if err != nil {
		return SchemaReceipt{}, err
	}
	if record.Intended != nil && observed == *record.Intended {
		return observed, nil
	}
	if observed != record.Before {
		return SchemaReceipt{}, ErrDurabilityUnknown
	}
	if record.Before.Schema == 0 && !equalJSON(current, checkpoint) {
		return SchemaReceipt{}, ErrDurabilityUnknown
	}
	files := &heldMigrationFiles{repositoryFiles: h.files, holder: h, record: &record}
	store, err := openWithFiles(ctx, h.directory, files, heldWriterBorrow{})
	if err != nil {
		if errors.Is(err, ErrDurabilityUnknown) {
			h.poisoned = true
		}
		return SchemaReceipt{}, err
	}
	if err := store.Close(); err != nil {
		return SchemaReceipt{}, err
	}
	current, err = h.files.ReadLegacy(ctx)
	if err != nil {
		return SchemaReceipt{}, err
	}
	return inspectBundle(current)
}
func (h *HeldRepository) Inspect(ctx context.Context) (SchemaReceipt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return SchemaReceipt{}, ErrClosed
	}
	bundle, err := h.files.ReadLegacy(ctx)
	if err != nil {
		return SchemaReceipt{}, err
	}
	return inspectBundle(bundle)
}

// Restore is installer-only. Its caller must verify the durable pre-operational
// fence first. A normal Store has no restore method and never restores on open.
func (h *HeldRepository) Restore(ctx context.Context, txID string, reference CheckpointRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	if h.poisoned {
		return ErrDurabilityUnknown
	}
	record, err := h.readRecord(txID)
	if err != nil {
		return err
	}
	if record.Ref != reference {
		return ErrCorruptState
	}
	bundle, err := h.readCheckpoint(ctx, record)
	if err != nil {
		return err
	}
	// Capture every expected detach hash in the held transaction before any
	// mutation. On retry, even an absent name must have a verified archive AND
	// committed receipt; presence alone never proves an uncertain prior move.
	if !record.RestorePrepared {
		current, err := h.files.readBundle(ctx, false)
		if err != nil {
			return err
		}
		record.RestoreDetaches = map[string]restoreDetach{}
		for name, file := range legacyFiles(bundle) {
			if file.Present {
				continue
			}
			observed := legacyFiles(current)[name]
			if observed.Present {
				record.RestoreDetaches[name] = restoreDetach{SHA256: checksum(observed.Bytes), Bytes: int64(len(observed.Bytes))}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		record.RestorePrepared = true
		if err := h.writeRecord(record); err != nil {
			return err
		}
	}
	for name, detach := range record.RestoreDetaches {
		file, ok := legacyFiles(bundle)[name]
		if !ok || file.Present || !validChecksum(detach.SHA256) || detach.Bytes < 0 || detach.Bytes > canonicalFileLimit {
			return ErrCorruptState
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, outcome, err := h.files.data.DetachFile(name, detach.SHA256, detach.Bytes)
		if outcome != securefs.Committed || err != nil {
			if outcome != securefs.Unchanged {
				h.poisoned = true
				return errors.Join(ErrDurabilityUnknown, err)
			}
			if err == nil {
				err = ErrCorruptState
			}
			return err
		}
	}
	for name, file := range legacyFiles(bundle) {
		if !file.Present {
			if _, err := h.files.data.ReadFile(name, 1); !errors.Is(err, fs.ErrNotExist) {
				h.poisoned = true
				return ErrDurabilityUnknown
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := h.files.data.ReplaceFile(name, file.Bytes)
		if state != securefs.Committed || err != nil {
			if state != securefs.Unchanged {
				h.poisoned = true
				return errors.Join(ErrDurabilityUnknown, err)
			}
			if err == nil {
				err = ErrCorruptState
			}
			return err
		}
	}
	for name, file := range legacyFiles(bundle) {
		if !file.Present {
			continue
		}
		copied, err := h.files.data.ReadFile(name, int64(len(file.Bytes)+1))
		if err != nil {
			return err
		}
		if !bytes.Equal(copied, file.Bytes) {
			h.poisoned = true
			return ErrDurabilityUnknown
		}
	}
	record.RestoreCompleted = true
	return h.writeRecord(record)
}
func (h *HeldRepository) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	err := h.writer.Close()
	if err == nil {
		h.closed = true
	}
	return err
}
