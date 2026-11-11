package installtx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func (f *ProtectedFS) Stage(ctx context.Context, source Source, expected Manifest) (result StagedPayload, failure error) {
	if f == nil || !filepath.IsAbs(source.AbsolutePath) || expected.Schema != 1 || expected.Kind != "tenebra-install-payload" ||
		!safeToken(expected.Version, 128) || !hexValue(expected.SourceSHA, 40) {
		return result, ErrProtocol
	}
	if f.policy.BoundArchiveSHA256 != "" && source.Layout != "tauri_app_archive" {
		return result, ErrSignature
	}
	if f.policy.Trust == "operator_dev" && !f.policy.AcknowledgeUntrusted {
		return result, ErrProtocol
	}
	archive := oneOf(source.Layout, "tauri_app_archive", "deb_archive")
	if source.Layout == "tauri_app_archive" && f.policy.Target != "darwin-universal" || source.Layout == "deb_archive" && f.policy.Target != "linux-amd64" {
		return result, ErrProtocol
	}
	if !oneOf(source.Layout, "flat", "installed_package", "tauri_deb_tree", "tauri_app_archive", "deb_archive") {
		return result, securefs.ErrUnsupported
	}
	if source.Layout == "installed_package" && (f.policy.Trust != "package_manager" || source.AbsolutePath != "/usr/lib/tenebra/package-payload") {
		return result, ErrProtocol
	}
	if f.policy.Trust == "package_manager" && source.Layout != "installed_package" {
		return result, ErrProtocol
	}
	f.mu.Lock()
	if f.reserved+len(f.stages) >= 32 {
		f.mu.Unlock()
		return result, securefs.ErrLimit
	}
	f.reserved++
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.reserved--; f.mu.Unlock() }()
	defer func() {
		if failure != nil && result.StageID != "" {
			failure = errors.Join(failure, f.RemoveOwnedStage(context.WithoutCancel(ctx), result.StageID))
			result = StagedPayload{}
		}
	}()
	directory := source.AbsolutePath
	if archive {
		directory = filepath.Dir(directory)
	}
	input, err := securefs.OpenRoot(directory, securefs.Policy{Mode: securefs.OperatorInput, Principal: f.policy.OperatorPrincipal}, false)
	if err != nil {
		return result, err
	}
	defer input.Close()
	copied, err := f.releases.NewStage("input-")
	if err != nil {
		return result, err
	}
	defer func() { failure = errors.Join(failure, copied.Close()) }()
	if archive {
		err = securefs.CopyFilesToStage(ctx, input, copied, []string{filepath.Base(source.AbsolutePath)}, securefs.Limits{MaxFiles: 1, MaxBytes: MaxContainerBytes})
	} else {
		err = securefs.CopyToStage(ctx, input, copied, securefs.Limits{MaxFiles: MaxArchiveEntries, MaxBytes: MaxExpandedBytes})
	}
	if err != nil {
		return result, err
	}
	var signature []byte
	if f.policy.Trust == "release" {
		if !filepath.IsAbs(source.SignaturePath) {
			return result, ErrSignature
		}
		signatures, err := securefs.OpenRoot(filepath.Dir(source.SignaturePath), securefs.Policy{Mode: securefs.OperatorInput, Principal: f.policy.OperatorPrincipal}, false)
		if err != nil {
			return result, err
		}
		signature, err = signatures.ReadFile(filepath.Base(source.SignaturePath), signatureTextLimit)
		closeErr := signatures.Close()
		if err != nil || closeErr != nil {
			return result, errors.Join(err, closeErr)
		}
	}
	lease, err := copied.Root().LeaseTree(ctx, securefs.Limits{MaxFiles: MaxArchiveEntries, MaxBytes: MaxExpandedBytes}, nil)
	if err != nil {
		return result, err
	}
	defer func() { failure = errors.Join(failure, lease.Close()) }()
	if archive {
		return f.stageArchive(ctx, lease, filepath.Base(source.AbsolutePath), source.Layout, signature, expected)
	}
	paths := rolePaths(source.Layout)
	var manifest Manifest
	if f.policy.Trust == "operator_dev" {
		manifest = Manifest{Schema: 1, Kind: "tenebra-install-payload", Version: expected.Version, SourceSHA: expected.SourceSHA, Target: f.policy.Target}
		manifest.Files, err = manifestFilesFromLease(ctx, lease, paths)
		if err != nil {
			return result, err
		}
	} else {
		body, err := lease.ReadFile("payload.json", MaxMetadataBytes)
		if err != nil {
			return result, err
		}
		manifest, err = DecodeManifest(body)
		if err != nil {
			return result, err
		}
		if f.policy.Trust == "release" {
			if err := VerifyDetached(bytes.NewReader(body), signature, f.policy.ReleasePublicKey); err != nil {
				return result, err
			}
		}
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		return result, err
	}
	if manifest.Version != expected.Version || manifest.SourceSHA != expected.SourceSHA || manifest.Target != f.policy.Target {
		return result, ErrProtocol
	}
	if len(expected.Files) > 0 {
		wanted, err := CanonicalManifest(expected)
		if err != nil || !bytes.Equal(wanted, canonical) {
			return result, ErrProtocol
		}
	}
	return f.stagePayload(ctx, lease, paths, manifest, f.policy.Trust)
}

func manifestFilesFromLease(ctx context.Context, lease *securefs.PathLease, paths map[string]string) ([]ManifestFile, error) {
	var files []ManifestFile
	var expanded int64
	for name, mode := range payloadRoles {
		reader, err := lease.OpenFile(paths[name], MaxExpandedBytes-expanded)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		size, readErr := io.Copy(hash, contextReader{ctx: ctx, Reader: reader})
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		expanded += size
		files = append(files, ManifestFile{Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size, Mode: mode})
	}
	return files, nil
}

func (f *ProtectedFS) stagePayload(ctx context.Context, lease *securefs.PathLease, paths map[string]string, manifest Manifest, provenance string) (result StagedPayload, failure error) {
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		return result, err
	}
	output, err := f.releases.NewStage("runtime-")
	if err != nil {
		return result, err
	}
	keep := false
	defer func() {
		if !keep {
			failure = errors.Join(failure, output.Close())
		}
	}()
	for _, file := range manifest.Files {
		reader, err := lease.OpenFile(paths[file.Name], file.Size)
		if err != nil {
			return result, err
		}
		hash := sha256.New()
		mode := uint32(0600)
		if file.Mode == 0755 {
			mode = 0700
		}
		counted := &sourceCounter{Reader: reader}
		outcome, copyErr := output.Root().ReplaceStream(ctx, file.Name, io.TeeReader(counted, hash), file.Size, mode)
		closeErr := reader.Close()
		if copyErr != nil || closeErr != nil || outcome != securefs.Committed {
			return result, errors.Join(copyErr, closeErr, ErrProtocol)
		}
		if counted.total != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return result, ErrProtocol
		}
	}
	if outcome, err := output.Root().ReplaceFile("payload.json", canonical); err != nil || outcome != securefs.Committed {
		return result, errors.Join(ErrProtocol, err)
	}
	hash := sha256.Sum256(canonical)
	id, err := newID("")
	if err != nil {
		return result, err
	}
	result = StagedPayload{StageID: "st1_" + id, Runtime: RuntimeRef{ArtifactSHA256: hex.EncodeToString(hash[:]), Version: manifest.Version, HelperSHA256: f.policy.HelperSHA256, Provenance: provenance}, Manifest: manifest}
	cached := result
	cached.Manifest.Files = append([]ManifestFile{}, manifest.Files...)
	f.mu.Lock()
	if _, exists := f.stages[result.StageID]; exists {
		f.mu.Unlock()
		return StagedPayload{}, ErrConflict
	}
	f.stages[result.StageID] = stagedRuntime{stage: output, payload: cached}
	keep = true
	f.mu.Unlock()
	return result, nil
}

type sourceCounter struct {
	io.Reader
	total int64
}

func (r *sourceCounter) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.total += int64(n)
	}
	return n, err
}
func rolePaths(layout string) map[string]string {
	result := map[string]string{}
	for name := range payloadRoles {
		path := name
		if layout == "tauri_deb_tree" {
			if name == "tenebra-core" {
				path = "usr/bin/tenebra-core"
			} else {
				path = "usr/lib/Tenebra/resources/" + name
			}
		}
		result[name] = path
	}
	return result
}
