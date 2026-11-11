package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

const settingsFileLimit = 1 << 20
const hintsFileLimit = 4 << 20
const backupByteLimit = canonicalFileLimit + settingsFileLimit + hintsFileLimit + (64 << 10)

type rootRepositoryFiles struct{ data, backup *securefs.Root }

func Open(ctx context.Context, data, backupRoot *securefs.Root) (*Store, error) {
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
	return openWithFiles(ctx, directory, &rootRepositoryFiles{data: data, backup: backupRoot}, writer)
}

func (f *rootRepositoryFiles) ReadLegacy(ctx context.Context) (LegacyBundle, error) {
	return f.readBundle(ctx, true)
}

// A held checkpoint preserves all three files, even when obsolete side files
// are no longer business inputs. Normal schema-2 opening never reads them.
func (f *rootRepositoryFiles) readBundle(ctx context.Context, canonicalOnly bool) (LegacyBundle, error) {
	read := func(name string, max int64) (LegacyFile, error) {
		if err := ctx.Err(); err != nil {
			return LegacyFile{}, err
		}
		data, err := f.data.ReadFile(name, max)
		if errors.Is(err, fs.ErrNotExist) {
			return LegacyFile{}, nil
		}
		if err != nil {
			return LegacyFile{}, err
		}
		return LegacyFile{true, data}, nil
	}
	var bundle LegacyBundle
	var err error
	bundle.Profiles, err = read("profiles.json", canonicalFileLimit)
	if err != nil {
		return LegacyBundle{}, err
	}
	if value := bytes.TrimSpace(bundle.Profiles.Bytes); canonicalOnly && bundle.Profiles.Present && len(value) > 0 && value[0] == '{' {
		// A versioned root, including an unknown/corrupt one, never opens stale
		// legacy preferences/hints as an alternate source of truth.
		return bundle, nil
	}
	bundle.Settings, err = read("settings.json", settingsFileLimit)
	if err != nil {
		return LegacyBundle{}, err
	}
	bundle.LastGood, err = read("lastgood.json", hintsFileLimit)
	if err != nil {
		return LegacyBundle{}, err
	}
	return bundle, nil
}
func (f *rootRepositoryFiles) ReplaceCanonical(ctx context.Context, data []byte) (securefs.CommitState, error) {
	if err := ctx.Err(); err != nil {
		return securefs.Unchanged, err
	}
	if len(data) > canonicalFileLimit {
		return securefs.Unchanged, securefs.ErrLimit
	}
	return f.data.ReplaceFile("profiles.json", data)
}

type backupFile struct {
	Present bool   `json:"present"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}
type backupManifest struct {
	Version int                   `json:"version"`
	Files   map[string]backupFile `json:"files"`
}

func legacyFiles(bundle LegacyBundle) map[string]LegacyFile {
	return map[string]LegacyFile{"profiles.json": bundle.Profiles, "settings.json": bundle.Settings, "lastgood.json": bundle.LastGood}
}
func manifestFor(bundle LegacyBundle) (backupManifest, []byte, error) {
	manifest := backupManifest{Version: 1, Files: map[string]backupFile{}}
	for name, file := range legacyFiles(bundle) {
		if !file.Present && len(file.Bytes) > 0 {
			return backupManifest{}, nil, ErrCorruptState
		}
		hash := sha256.Sum256(file.Bytes)
		manifest.Files[name] = backupFile{file.Present, len(file.Bytes), hex.EncodeToString(hash[:])}
	}
	data, err := json.Marshal(manifest)
	return manifest, data, err
}
func backupName(data []byte) string {
	hash := sha256.Sum256(data)
	return "legacy-" + hex.EncodeToString(hash[:])
}

func (f *rootRepositoryFiles) verifyExistingBackup(ctx context.Context, name string, bundle LegacyBundle, manifest []byte) (bool, error) {
	data, err := f.backup.ReadFile(name+"/manifest.json", 64<<10)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(data, manifest) {
		return false, ErrCorruptState
	}
	for fileName, file := range legacyFiles(bundle) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		max := int64(len(file.Bytes))
		if max == 0 {
			max = 1
		}
		data, err := f.backup.ReadFile(name+"/"+fileName, max)
		if !file.Present {
			if !errors.Is(err, fs.ErrNotExist) {
				return false, ErrCorruptState
			}
			continue
		}
		if err != nil {
			return false, err
		}
		if !bytes.Equal(data, file.Bytes) {
			return false, ErrCorruptState
		}
	}
	return true, nil
}

func (f *rootRepositoryFiles) Backup(ctx context.Context, bundle LegacyBundle) (failure error) {
	_, manifest, err := manifestFor(bundle)
	if err != nil {
		return err
	}
	name := backupName(manifest)
	if exists, err := f.verifyExistingBackup(ctx, name, bundle, manifest); err != nil || exists {
		return err
	}
	stage, err := f.backup.NewStage("legacy-stage-")
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, stage.Close()) }()
	for fileName, file := range legacyFiles(bundle) {
		if !file.Present {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := stage.Root().ReplaceFile(fileName, file.Bytes)
		if err != nil {
			return err
		}
		if state != securefs.Committed {
			return ErrDurabilityUnknown
		}
	}
	if state, err := stage.Root().ReplaceFile("manifest.json", manifest); err != nil {
		return err
	} else if state != securefs.Committed {
		return ErrDurabilityUnknown
	}
	verify := func(ctx context.Context, lease *securefs.PathLease) error {
		expected := map[string][]byte{"manifest.json": manifest}
		for name, file := range legacyFiles(bundle) {
			if file.Present {
				expected[name] = file.Bytes
			}
		}
		if len(lease.Files()) != len(expected) {
			return ErrCorruptState
		}
		for _, name := range lease.Files() {
			data, exists := expected[name]
			if !exists {
				return ErrCorruptState
			}
			limit := int64(len(data))
			if limit == 0 {
				limit = 1
			}
			copied, err := lease.ReadFile(name, limit)
			if err != nil {
				return err
			}
			if !bytes.Equal(data, copied) {
				return ErrCorruptState
			}
		}
		return nil
	}
	published, state, err := stage.Publish(ctx, name, securefs.Limits{MaxFiles: 8, MaxBytes: backupByteLimit}, verify)
	if published != nil {
		err = errors.Join(err, published.Close())
	}
	if err != nil || state != securefs.Committed {
		if errors.Is(err, fs.ErrExist) {
			if exists, check := f.verifyExistingBackup(ctx, name, bundle, manifest); check != nil {
				return check
			} else if exists {
				return nil
			}
		}
		if err == nil {
			err = ErrDurabilityUnknown
		}
		return err
	}
	if exists, err := f.verifyExistingBackup(ctx, name, bundle, manifest); err != nil {
		return err
	} else if !exists {
		return ErrCorruptState
	}
	return nil
}
