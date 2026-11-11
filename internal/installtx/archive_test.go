package installtx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func archiveFixturePath(layout, role string) string {
	if layout == "tauri_app_archive" {
		if role == "tenebra-core" {
			return "Tenebra.app/Contents/MacOS/tenebra-core"
		}
		return "Tenebra.app/Contents/Resources/resources/" + role
	}
	if role == "payload.json" {
		return "usr/lib/tenebra/package-payload/payload.json"
	}
	return rolePaths("tauri_deb_tree")[role]
}
func gzipFixture(t *testing.T, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func signedArchiveFixture(t *testing.T, layout, mutation string) (*ProtectedFS, Source, *securefs.Root, Manifest) {
	t.Helper()
	stager, source, input, manifest := stagingFixture(t, "flat", "release", false)
	if layout == "tauri_app_archive" {
		manifest.Target = "darwin-universal"
		stager.policy.Target = manifest.Target
	}
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	entry := func(name string, body []byte, kind byte, link string) {
		t.Helper()
		header := &tar.Header{Name: name, Mode: 0644, Size: int64(len(body)), Typeflag: kind, Linkname: link, Format: tar.FormatUSTAR}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(body) > 0 {
			if _, err := writer.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if mutation == "traversal" {
		entry("../tenebra-core", []byte("forged"), tar.TypeReg, "")
	}
	if mutation == "symlink" {
		entry("unexpected-link", nil, tar.TypeSymlink, "/etc/forbidden")
	}
	if mutation == "entries" {
		for i := 0; i < MaxArchiveEntries+1; i++ {
			entry(fmt.Sprintf("ignored/%d", i), nil, tar.TypeReg, "")
		}
	}
	for _, file := range manifest.Files {
		body, err := input.ReadFile(file.Name, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		entry(archiveFixturePath(layout, file.Name), body, tar.TypeReg, "")
	}
	if mutation == "duplicate-role" {
		name := archiveFixturePath(layout, "tenebra-core")
		if layout == "deb_archive" {
			name = "usr/lib/tenebra/package-payload/tenebra-core"
		}
		entry(name, []byte("duplicate"), tar.TypeReg, "")
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entry(archiveFixturePath(layout, "payload.json"), canonical, tar.TypeReg, "")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	container := gzipFixture(t, raw.Bytes())
	filename := "payload.tar.gz"
	if layout == "deb_archive" {
		var deb bytes.Buffer
		deb.WriteString("!<arch>\n")
		member := func(name string, body []byte) {
			fmt.Fprintf(&deb, "%-16s%-12d%-6d%-6d%-8s%-10d%s", name+"/", 0, 0, 0, "100644", len(body), "\x60\n")
			deb.Write(body)
			if len(body)%2 != 0 {
				deb.WriteByte('\n')
			}
		}
		member("debian-binary", []byte("2.0\n"))
		member("control.tar.gz", gzipFixture(t, make([]byte, 1024)))
		member("data.tar.gz", container)
		container = deb.Bytes()
		filename = "payload.deb"
	}
	_, signature, _ := signatureFixture(t, "ED", container)
	if mutation == "changed-outer" {
		container = append(append([]byte{}, container...), byte('!'))
	}
	for name, body := range map[string][]byte{filename: container, "archive.sig": signature} {
		if state, err := input.ReplaceFile(name, body); err != nil || state != securefs.Committed {
			t.Fatal(state, err)
		}
	}
	source.Layout = layout
	source.AbsolutePath = filepath.Join(source.AbsolutePath, filename)
	source.SignaturePath = filepath.Join(filepath.Dir(source.AbsolutePath), "archive.sig")
	return stager, source, input, manifest
}
func TestCopiedSignedArchivesUsePrivateBytesAndCanonicalArtifact(t *testing.T) {
	for _, layout := range []string{"tauri_app_archive", "deb_archive"} {
		t.Run(layout, func(t *testing.T) {
			ctx := context.Background()
			stager, source, input, manifest := signedArchiveFixture(t, layout, "")
			payload, err := stager.Stage(ctx, source, manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := input.ReplaceFile(filepath.Base(source.AbsolutePath), []byte("changed original container")); err != nil {
				t.Fatal(err)
			}
			runtime, err := stager.PublishRuntime(ctx, payload)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := artifactHash(manifest)
			if err != nil || runtime.ArtifactSHA256 != artifact {
				t.Fatal("outer archive became artifact identity", err)
			}
			if _, err := stager.ReadRuntimeManifest(ctx, runtime); err != nil {
				t.Fatal(err)
			}
			if err := stager.RemoveOwnedStage(ctx, payload.StageID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSourceArchivesRejectTraversalLinksDuplicatesAndOuterTamper(t *testing.T) {
	for _, layout := range []string{"tauri_app_archive", "deb_archive"} {
		for _, mutation := range []string{"traversal", "symlink", "duplicate-role", "changed-outer", "entries"} {
			t.Run(layout+"/"+mutation, func(t *testing.T) {
				stager, source, _, manifest := signedArchiveFixture(t, layout, mutation)
				if _, err := stager.Stage(context.Background(), source, manifest); err == nil {
					t.Fatal("unsafe archive accepted")
				}
				requireNoPublishedSource(t, stager, manifest)
			})
		}
	}
}
func TestArchiveExpandedBudgetAndHiddenMetadataRefuse(t *testing.T) {
	budget := &archiveBudget{expanded: MaxExpandedBytes - 1}
	if _, err := io.ReadAll(&expandedReader{ctx: context.Background(), Reader: bytes.NewReader([]byte("xx")), budget: budget}); err == nil {
		t.Fatal("expanded budget overflow accepted")
	}
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{Name: "header", Mode: 0644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	header := append([]byte{}, raw.Bytes()[:512]...)
	header[156] = tar.TypeXHeader
	if err := walkTar(context.Background(), bytes.NewReader(header), &archiveBudget{}, func(*tar.Header, io.Reader) error { t.Fatal("hidden metadata reached selected handler"); return nil }); err == nil {
		t.Fatal("hidden PAX entry accepted")
	}
}

func TestCopiedOpaqueMacPayloadUsesInnerRoles(t *testing.T) {
	for _, name := range []string{"resealed_outer_sidecars", "missing_inner_role_cannot_use_outer_copy", "changed_inner_gzip", "different_compiled_payload"} {
		t.Run(name, func(t *testing.T) {
			missingRole := name == "missing_inner_role_cannot_use_outer_copy"
			ctx := context.Background()
			stager, source, input, manifest := stagingFixture(t, "flat", "release", false)
			manifest.Target = "darwin-universal"
			stager.policy.Target = manifest.Target
			canonical, err := CanonicalManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var inside, outside bytes.Buffer
			inner, outer := tar.NewWriter(&inside), tar.NewWriter(&outside)
			write := func(w *tar.Writer, path string, data []byte) {
				t.Helper()
				if err := w.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(data)), Format: tar.FormatUSTAR}); err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			write(inner, "payload.json", canonical)
			roles := make(map[string][]byte, len(manifest.Files))
			for _, file := range manifest.Files {
				data, err := input.ReadFile(file.Name, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				roles[file.Name] = data
				if !missingRole || file.Name != "tenebra-core" {
					write(inner, file.Name, data)
				}
			}
			if err := inner.Close(); err != nil {
				t.Fatal(err)
			}
			core := roles["tenebra-core"]
			if !missingRole {
				core = []byte("outer application sidecar was resealed")
			}
			write(outer, archiveFixturePath("tauri_app_archive", "tenebra-core"), core)
			compressed := gzipFixture(t, inside.Bytes())
			archiveHash := sha256.Sum256(compressed)
			payloadHash := sha256.Sum256(canonical)
			stager.policy.BoundArchiveSHA256 = hex.EncodeToString(archiveHash[:])
			stager.policy.BoundPayloadSHA256 = hex.EncodeToString(payloadHash[:])
			if name == "changed_inner_gzip" {
				compressed[4] ^= 1 // Change MTIME without changing any expanded role.
			}
			if name == "different_compiled_payload" {
				payloadHash[0] ^= 1
				stager.policy.BoundPayloadSHA256 = hex.EncodeToString(payloadHash[:])
			}
			write(outer, macPayloadArchive, compressed)
			for _, file := range manifest.Files {
				if file.Name != "tenebra-core" {
					write(outer, archiveFixturePath("tauri_app_archive", file.Name), roles[file.Name])
				}
			}
			write(outer, archiveFixturePath("tauri_app_archive", "payload.json"), canonical)
			if err := outer.Close(); err != nil {
				t.Fatal(err)
			}
			container := gzipFixture(t, outside.Bytes())
			_, sig, _ := signatureFixture(t, "ED", container)
			for filename, data := range map[string][]byte{"Tenebra.app.tar.gz": container, "archive.sig": sig} {
				if state, err := input.ReplaceFile(filename, data); state != securefs.Committed || err != nil {
					t.Fatal(state, err)
				}
			}
			source.Layout = "tauri_app_archive"
			source.SignaturePath = filepath.Join(source.AbsolutePath, "archive.sig")
			source.AbsolutePath = filepath.Join(source.AbsolutePath, "Tenebra.app.tar.gz")
			payload, err := stager.Stage(ctx, source, manifest)
			if missingRole {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("missing inner role did not fail closed: %v", err)
				}
				requireNoPublishedSource(t, stager, manifest)
				return
			}
			if name == "changed_inner_gzip" || name == "different_compiled_payload" {
				if !errors.Is(err, ErrSignature) {
					t.Fatalf("signed outer artifact used a different compiled payload: %v", err)
				}
				requireNoPublishedSource(t, stager, manifest)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stager.RemoveOwnedStage(ctx, payload.StageID) })
			runtime, err := stager.PublishRuntime(ctx, payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stager.ReadRuntimeManifest(ctx, runtime); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNestedArchiveSharesExpandedBudget(t *testing.T) {
	ctx := context.Background()
	budget := &archiveBudget{expanded: MaxExpandedBytes - 1}
	outer := &expandedReader{ctx: ctx, Reader: bytes.NewReader([]byte("x")), budget: budget}
	inner := &expandedReader{ctx: ctx, Reader: outer, budget: budget}
	if _, err := inner.Read(make([]byte, 1)); !errors.Is(err, securefs.ErrLimit) || budget.expanded > MaxExpandedBytes {
		t.Fatalf("nested readers exceeded their shared expansion budget: %d, %v", budget.expanded, err)
	}
}
