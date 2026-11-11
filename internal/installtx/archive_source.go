package installtx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

const macPayloadArchive = "Tenebra.app/Contents/Resources/resources/payload.tar.gz"

func archiveRole(name, layout string) string {
	if name == "payload.json" {
		return name
	}
	if layout == "tauri_app_archive" {
		if name == "Tenebra.app/Contents/Resources/resources/payload.json" {
			return "payload.json"
		}
		if name == "Tenebra.app/Contents/MacOS/tenebra-core" {
			return "tenebra-core"
		}
		for role := range payloadRoles {
			if role != "tenebra-core" && name == "Tenebra.app/Contents/Resources/resources/"+role {
				return role
			}
		}
	} else if layout == "deb_archive" {
		if name == "usr/lib/tenebra/package-payload/payload.json" {
			return "payload.json"
		}
		for role := range payloadRoles {
			if name == "usr/lib/tenebra/package-payload/"+role || name == rolePaths("tauri_deb_tree")[role] {
				return role
			}
		}
	}
	return ""
}
func (f *ProtectedFS) stageArchive(ctx context.Context, lease *securefs.PathLease, container, layout string, signature []byte, expected Manifest) (result StagedPayload, failure error) {
	if f.policy.Trust == "package_manager" {
		return result, ErrProtocol
	}
	if f.policy.Trust == "release" {
		stream, err := lease.OpenFile(container, MaxContainerBytes)
		if err != nil {
			return result, err
		}
		verifyErr := VerifyDetached(contextReader{ctx: ctx, Reader: stream}, signature, f.policy.ReleasePublicKey)
		if err := errors.Join(verifyErr, stream.Close()); err != nil {
			return result, err
		}
	}
	stream, err := lease.OpenFile(container, MaxContainerBytes)
	if err != nil {
		return result, err
	}
	defer func() { failure = errors.Join(failure, stream.Close()) }()
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
	actual := Manifest{Schema: 1, Kind: "tenebra-install-payload", Version: expected.Version, SourceSHA: expected.SourceSHA, Target: f.policy.Target}
	selected := map[string]bool{}
	var metadata []byte
	budget := &archiveBudget{}
	stageRole := func(role string, entry *tar.Header, body io.Reader) error {
		if role == "" {
			return nil
		}
		if selected[role] || len(selected) >= MaxSelectedRoles {
			return ErrProtocol
		}
		selected[role] = true
		if role == "payload.json" {
			if entry.Size > MaxMetadataBytes {
				return securefs.ErrLimit
			}
			var err error
			metadata, err = io.ReadAll(body)
			if err != nil || int64(len(metadata)) != entry.Size {
				return errors.Join(ErrProtocol, err)
			}
			return nil
		}
		mode := uint32(0600)
		if payloadRoles[role] == 0755 {
			mode = 0700
		}
		hash := sha256.New()
		counter := &sourceCounter{Reader: body}
		state, err := output.Root().ReplaceStream(ctx, role, io.TeeReader(counter, hash), entry.Size, mode)
		if err != nil || state != securefs.Committed || counter.total != entry.Size {
			return errors.Join(ErrProtocol, err)
		}
		actual.Files = append(actual.Files, ManifestFile{Name: role, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: entry.Size, Mode: payloadRoles[role]})
		return nil
	}
	readTar := func(input io.Reader, compressed bool, visit func(*tar.Header, io.Reader) error) (failure error) {
		if !compressed {
			return walkTar(ctx, input, budget, visit)
		}
		zipped, err := gzip.NewReader(contextReader{ctx: ctx, Reader: input})
		if err != nil {
			return err
		}
		defer func() { failure = errors.Join(failure, zipped.Close()) }()
		return walkTar(ctx, zipped, budget, visit)
	}
	opaquePayload := false
	visit := func(entry *tar.Header, body io.Reader) error {
		if layout == "tauri_app_archive" && entry.Name == macPayloadArchive {
			if entry.Size > MaxContainerBytes {
				return securefs.ErrLimit
			}
			opaquePayload = true
			// The outer app can contain resealed copies of the same roles.
			// Discard any copies collected for a legacy flat app before reading
			// the signed inner archive. Never use them to fill missing roles.
			if len(selected) != 0 {
				if err := output.Close(); err != nil {
					return err
				}
				inner, err := f.releases.NewStage("runtime-")
				if err != nil {
					return err
				}
				output = inner
				clear(selected)
				actual.Files = nil
				metadata = nil
			}
			archiveHash := sha256.New()
			archive := &sourceCounter{Reader: io.TeeReader(body, archiveHash)}
			if err := readTar(archive, true, func(inner *tar.Header, content io.Reader) error {
				if _, known := payloadRoles[inner.Name]; !known && inner.Name != "payload.json" {
					return ErrProtocol
				}
				return stageRole(inner.Name, inner, content)
			}); err != nil {
				return err
			}
			if archive.total != entry.Size || f.policy.BoundArchiveSHA256 != "" && hex.EncodeToString(archiveHash.Sum(nil)) != f.policy.BoundArchiveSHA256 {
				return ErrSignature
			}
			return nil
		}
		if opaquePayload {
			return nil
		}
		return stageRole(archiveRole(entry.Name, layout), entry, body)
	}
	if layout == "tauri_app_archive" {
		err = readTar(stream, true, visit)
	} else {
		err = walkDeb(ctx, stream, budget, func(name string, data io.Reader) error {
			return readTar(data, name == "data.tar.gz", visit)
		})
	}
	if err != nil {
		return result, err
	}
	canonical, err := CanonicalManifest(actual)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(canonical)
	if f.policy.BoundArchiveSHA256 != "" && (!opaquePayload || hex.EncodeToString(sum[:]) != f.policy.BoundPayloadSHA256) {
		return result, ErrSignature
	}
	if f.policy.Trust == "release" || opaquePayload {
		signed, err := DecodeManifest(metadata)
		if err != nil {
			return result, err
		}
		declared, err := CanonicalManifest(signed)
		if err != nil || !bytes.Equal(canonical, declared) {
			return result, ErrProtocol
		}
	}
	if len(expected.Files) > 0 {
		claimed, err := CanonicalManifest(expected)
		if err != nil || !bytes.Equal(canonical, claimed) {
			return result, ErrProtocol
		}
	}
	if state, err := output.Root().ReplaceFile("payload.json", canonical); err != nil || state != securefs.Committed {
		return result, errors.Join(ErrProtocol, err)
	}
	id, err := newID("")
	if err != nil {
		return result, err
	}
	result = StagedPayload{StageID: "st1_" + id, Runtime: RuntimeRef{ArtifactSHA256: hex.EncodeToString(sum[:]), Version: actual.Version, HelperSHA256: f.policy.HelperSHA256, Provenance: f.policy.Trust}, Manifest: actual}
	cached := result
	cached.Manifest.Files = append([]ManifestFile{}, actual.Files...)
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
