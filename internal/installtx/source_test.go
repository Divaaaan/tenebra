package installtx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func stagingFixture(t *testing.T, layout, trust string, ack bool) (*ProtectedFS, Source, *securefs.Root, Manifest) {
	t.Helper()
	root := privateInstallerRoot(t)
	releases, err := root.OpenDir("releases", securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := root.OpenDir("maintenance", securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := openAccountJournalFile(maintenance)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
		if err := maintenance.Close(); err != nil {
			t.Error(err)
		}
		if err := releases.Close(); err != nil {
			t.Error(err)
		}
	})
	input := privateInstallerRoot(t)
	manifest := Manifest{Schema: 1, Kind: "tenebra-install-payload", Version: "0.6.0", SourceSHA: strings.Repeat("a", 40), Target: "linux-amd64"}
	for name, mode := range payloadRoles {
		body := []byte("SYNTHETIC_ONLY " + name + "\n")
		hash := sha256.Sum256(body)
		path := rolePaths(layout)[name]
		if directory := filepath.ToSlash(filepath.Dir(path)); directory != "." {
			parent, err := input.OpenDir(directory, securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := parent.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if outcome, err := input.ReplaceFile(path, body); outcome != securefs.Committed || err != nil {
			t.Fatal(outcome, err)
		}
		manifest.Files = append(manifest.Files, ManifestFile{Name: name, Mode: mode, Size: int64(len(body)), SHA256: hex.EncodeToString(hash[:])})
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, signature, key := signatureFixture(t, "ED", canonical)
	for name, body := range map[string][]byte{"payload.json": canonical, "payload.sig": signature} {
		if outcome, err := input.ReplaceFile(name, body); outcome != securefs.Committed || err != nil {
			t.Fatal(outcome, err)
		}
	}
	principal, err := CurrentOperatorPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	stager, err := newProtectedFS(releases, journal, SourcePolicy{Trust: trust, OperatorPrincipal: principal, ReleasePublicKey: key, HelperSHA256: strings.Repeat("b", 64), Target: "linux-amd64", AcknowledgeUntrusted: ack}, securefs.Policy{Mode: securefs.SameAccountPrivate})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := input.Path()
	if err != nil {
		t.Fatal(err)
	}
	return stager, Source{Layout: layout, AbsolutePath: directory, SignaturePath: filepath.Join(directory, "payload.sig")}, input, manifest
}
func requireNoPublishedSource(t *testing.T, stager *ProtectedFS, manifest Manifest) {
	t.Helper()
	hash, err := artifactHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if root, err := stager.releases.OpenDir(hash, stager.privatePolicy, false); !errors.Is(err, fs.ErrNotExist) {
		if root != nil {
			root.Close()
		}
		t.Fatalf("failed source published: %v", err)
	}
	if len(stager.stages) != 0 {
		t.Fatal("failed source retained executable candidate")
	}
}
func TestCopiedReleaseBytesAreAuthority(t *testing.T) {
	ctx := context.Background()
	stager, source, input, manifest := stagingFixture(t, "flat", "release", false)
	payload, err := stager.Stage(ctx, source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := input.ReplaceFile("tenebra-core", []byte("changed source after private copy")); outcome != securefs.Committed || err != nil {
		t.Fatal(outcome, err)
	}
	runtime, err := stager.PublishRuntime(ctx, payload)
	if err != nil {
		t.Fatal("private copied bytes were replaced by source-path reread", err)
	}
	observed, err := stager.ReadRuntimeManifest(ctx, runtime)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := CanonicalManifest(manifest)
	got, _ := CanonicalManifest(observed)
	if !bytes.Equal(want, got) || runtime.Provenance != "release" {
		t.Fatal("published authority differs")
	}
	if err := stager.RemoveOwnedStage(ctx, payload.StageID); err != nil {
		t.Fatal(err)
	}
}
func TestCopiedRuntimeTamperingRefusesPublication(t *testing.T) {
	ctx := context.Background()
	stager, source, _, manifest := stagingFixture(t, "flat", "release", false)
	payload, err := stager.Stage(ctx, source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	owned := stager.stages[payload.StageID]
	if outcome, err := owned.stage.Root().ReplaceFile("tenebra-core", []byte("forged copied bytes")); outcome != securefs.Committed || err != nil {
		t.Fatal(outcome, err)
	}
	if _, err := stager.PublishRuntime(ctx, payload); err == nil {
		t.Fatal("private candidate tamper published")
	}
	if err := stager.RemoveOwnedStage(ctx, payload.StageID); err != nil {
		t.Fatal(err)
	}
	requireNoPublishedSource(t, stager, manifest)
}
func TestSourceRejectsUntrustedObjects(t *testing.T) {
	for _, mutation := range []string{"body", "manifest-key-claim", "hardlink", "symlink"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			stager, source, input, manifest := stagingFixture(t, "flat", "release", false)
			switch mutation {
			case "body":
				if _, err := input.ReplaceFile("tenebra-core", []byte("forged unsigned body")); err != nil {
					t.Fatal(err)
				}
			case "manifest-key-claim":
				body, err := input.ReadFile("payload.json", MaxMetadataBytes)
				if err != nil {
					t.Fatal(err)
				}
				body = bytes.Replace(body, []byte(`"schema": 1`), []byte(`"schema": 1, "public_key": "forged-source-key"`), 1)
				if _, err := input.ReplaceFile("payload.json", body); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(source.AbsolutePath, "tenebra-core"), filepath.Join(source.AbsolutePath, "other-link")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				path := filepath.Join(source.AbsolutePath, "tenebra-core")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("sing-box", path); err != nil {
					t.Skipf("native fixture cannot create symlink: %v", err)
				}
			}
			if _, err := stager.Stage(ctx, source, manifest); err == nil {
				t.Fatal("untrusted source accepted")
			}
			requireNoPublishedSource(t, stager, manifest)
		})
	}
}
func TestDevModeStillContained(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-ack", true: "explicit-ack"}[ack], func(t *testing.T) {
			ctx := context.Background()
			stager, source, _, manifest := stagingFixture(t, "flat", "operator_dev", ack)
			payload, err := stager.Stage(ctx, source, manifest)
			if !ack {
				if err == nil {
					t.Fatal("unacknowledged dev input accepted")
				}
				requireNoPublishedSource(t, stager, manifest)
				return
			}
			if err != nil || payload.Runtime.Provenance != "operator_dev" {
				t.Fatal("dev provenance relabeled as vendor", err)
			}
			if _, err := stager.PublishRuntime(ctx, payload); err != nil {
				t.Fatal(err)
			}
			if err := stager.RemoveOwnedStage(ctx, payload.StageID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestDebTreeUsesActualLayout(t *testing.T) {
	ctx := context.Background()
	stager, source, _, manifest := stagingFixture(t, "tauri_deb_tree", "operator_dev", true)
	payload, err := stager.Stage(ctx, source, manifest)
	if err != nil {
		t.Fatal("actual usr/bin + usr/lib/Tenebra/resources layout refused", err)
	}
	runtime, err := stager.PublishRuntime(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stager.ReadRuntimeManifest(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	if err := stager.RemoveOwnedStage(ctx, payload.StageID); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSignedSizeAndPinnedKeyCannotBeForged(t *testing.T) {
	for _, kind := range []string{"signed-wrong-size", "different-key"} {
		t.Run(kind, func(t *testing.T) {
			stager, source, input, manifest := stagingFixture(t, "flat", "release", false)
			original := manifest
			original.Files = append([]ManifestFile{}, manifest.Files...)
			if kind == "signed-wrong-size" {
				manifest.Files[0].Size++
				body, err := CanonicalManifest(manifest)
				if err != nil {
					t.Fatal(err)
				}
				_, signature, _ := signatureFixture(t, "ED", body)
				if _, err := input.ReplaceFile("payload.json", body); err != nil {
					t.Fatal(err)
				}
				if _, err := input.ReplaceFile("payload.sig", signature); err != nil {
					t.Fatal(err)
				}
			} else {
				key, err := minisignPublicKey(stager.policy.ReleasePublicKey)
				if err != nil {
					t.Fatal(err)
				}
				key[10] ^= 1
				stager.policy.ReleasePublicKey = base64.StdEncoding.EncodeToString(key)
			}
			if _, err := stager.Stage(context.Background(), source, manifest); err == nil {
				t.Fatal("forged size/key accepted")
			}
			requireNoPublishedSource(t, stager, original)
		})
	}
}
