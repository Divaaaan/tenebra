package installtx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type legacySnapshotFS struct {
	fakeInstallFS
	prior    *RuntimeRef
	failure  error
	calls    int
	unlocked bool
}

func (f *legacySnapshotFS) SnapshotLegacy(context.Context) (*RuntimeRef, error) {
	f.calls++
	if f.disk.mu.TryLock() {
		f.unlocked = true
		f.disk.mu.Unlock()
	}
	f.disk.calls = append(f.disk.calls, "legacy-snapshot")
	return f.prior, f.failure
}

func legacyInstallerFixture() (*installDisk, *legacySnapshotFS, *Installer) {
	d := installerDisk()
	prior := *d.running
	prior.Version, prior.Provenance = "legacy", "legacy_root_owned"
	d.journal = Journal{Schema: 1, Generation: 1, Manager: "standalone"}
	d.running = &prior
	f := &legacySnapshotFS{fakeInstallFS: fakeInstallFS{d}, prior: &prior}
	return d, f, New(f, fakeInstallStore{d}, fakeInstallService{d}, fakeInstallProbe{d})
}

func TestLegacySnapshotStagesWithoutStoppingOrCheckpointing(t *testing.T) {
	d, files, installer := legacyInstallerFixture()
	request := installRequest(d)
	request.RestartAck = ""
	result, err := installer.Apply(context.Background(), request)
	if err != nil || result.Status.Phase != "pending" || files.calls != 1 || files.unlocked {
		t.Fatal("legacy snapshot was not staged under the transaction lock", result, err, files.calls, files.unlocked)
	}
	if !sameRuntime(d.journal.Active, files.prior) || !sameRuntime(d.journal.Transaction.PriorActive, files.prior) ||
		!sameRuntime(d.running, files.prior) || !d.admitted || len(d.checkpoints) != 0 || d.restoreCount != 0 {
		t.Fatal("pending legacy adoption changed the running installation or data", d.journal, d.calls)
	}
	if !reflect.DeepEqual(d.calls, []string{"legacy-snapshot", "stage", "publish", "journal:staged", "journal:pending"}) {
		t.Fatal("staging performed an activation action", d.calls)
	}
	// The old process can still write after staging. Activation must capture it.
	d.current.Revision = 42
	d.data["profile"] = "legacy-edit-after-staging"
	apply := ApplyRequest{Schema: 1, OperationID: "so1_" + strings.Repeat("4", 32), Action: "apply_pending", ExpectedPayloadSHA256: d.journal.Pending.ArtifactSHA256}
	result, err = installer.Apply(context.Background(), apply)
	if err != nil || result.Status.Phase != "committed_open" || d.journal.Transaction.Checkpoint.Revision != 42 ||
		d.data["profile"] != "legacy-edit-after-staging" || files.calls != 1 || d.restoreCount != 0 {
		t.Fatal("activation did not use the latest whole legacy checkpoint", result, err, d.calls)
	}
}

func TestLegacySnapshotFailurePreservesOldJobAndEmptyJournal(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		d, files, installer := legacyInstallerFixture()
		if invalid {
			files.prior.Provenance = "release"
		} else {
			files.failure = ErrUnknownService
		}
		before := cloneJournal(d.journal)
		if _, err := installer.Apply(context.Background(), installRequest(d)); err == nil {
			t.Fatal("failed or incorrectly labelled legacy snapshot was admitted")
		}
		if files.calls != 1 || files.unlocked || !reflect.DeepEqual(before, d.journal) || !d.admitted ||
			!sameRuntime(d.running, files.prior) || !reflect.DeepEqual(d.calls, []string{"legacy-snapshot"}) {
			t.Fatal("failed snapshot changed the prior job, journal or runtime", d.journal, d.calls)
		}
	}
}

func TestLegacySnapshotRecoveryRetainsClosedPriorWithoutInventingCommit(t *testing.T) {
	d, files, installer := legacyInstallerFixture()
	d.stageEdit = true
	d.crashPhase = "schema_ready"
	if _, err := installer.Apply(context.Background(), installRequest(d)); !errors.Is(err, errInstallerCrash) {
		t.Fatal("expected the pre-operational crash", err)
	}
	result, err := reconstructedInstaller(d).Recover(context.Background())
	if err != nil || result.Status.Phase != "rolled_back" || !sameRuntime(d.journal.Active, files.prior) || d.admitted || d.running != nil ||
		d.restoreCount != 1 || d.data["profile"] != "edited-while-staging" || d.journal.Transaction.OperationalCommitID != "" {
		t.Fatal("legacy rollback lost the whole snapshot or invented admission", result, err, d.journal, d.calls)
	}
}

func TestLegacySnapshotManifestRecordsUnknownVersionAndSource(t *testing.T) {
	m := installerDisk().manifest
	m.Kind, m.Version, m.SourceSHA = "tenebra-legacy-snapshot", "legacy", ""
	body, err := CanonicalManifest(m)
	if err != nil {
		t.Fatal("the root-owned snapshot requires explicit unknown build metadata", err)
	}
	decoded, err := DecodeManifest(body)
	if err != nil || decoded.Kind != m.Kind || decoded.Version != "legacy" || decoded.SourceSHA != "" {
		t.Fatal("snapshot metadata was relabelled as a release", decoded, err)
	}
	for _, alter := range []func(*Manifest){
		func(m *Manifest) { m.Version = "0.6.1" },
		func(m *Manifest) { m.SourceSHA = strings.Repeat("a", 40) },
		func(m *Manifest) { m.Target = "darwin-universal" },
	} {
		bad := m
		alter(&bad)
		if _, err := CanonicalManifest(bad); err == nil {
			t.Fatal("legacy snapshot accepted an unsupported build claim", bad)
		}
	}
}

func TestLegacySnapshotCopiedRolesRemainAuthority(t *testing.T) {
	files, _, input, original := stagingFixture(t, "flat", "release", false)
	ctx := context.Background()
	ref, err := files.snapshotLegacyInput(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Provenance != "legacy_root_owned" || ref.Version != "legacy" || len(files.stages) != 0 {
		t.Fatal("snapshot was relabelled or retained an executable staging tree", ref)
	}
	manifest, err := files.ReadRuntimeManifest(ctx, *ref)
	if err != nil || manifest.Kind != "tenebra-legacy-snapshot" || manifest.SourceSHA != "" {
		t.Fatal("protected snapshot could not be verified", manifest, err)
	}
	for _, before := range original.Files {
		found := false
		for _, copied := range manifest.Files {
			if copied == before {
				found = true
			}
		}
		if !found {
			t.Fatal("snapshot changed a selected runtime role", before)
		}
	}
	if outcome, err := input.ReplaceFile("tenebra-core", []byte("SYNTHETIC_REPLACEMENT")); outcome != securefs.Committed || err != nil {
		t.Fatal(outcome, err)
	}
	after, err := files.ReadRuntimeManifest(ctx, *ref)
	if err != nil || !reflect.DeepEqual(after, manifest) {
		t.Fatal("source replacement changed the protected snapshot", after, err)
	}
	forged := *ref
	forged.Provenance = "release"
	if _, err := files.ReadRuntimeManifest(ctx, forged); err == nil {
		t.Fatal("root-owned snapshot acquired vendor release provenance")
	}
}

func TestLegacySnapshotCannotBeClaimedBySignedInput(t *testing.T) {
	files, source, input, expected := stagingFixture(t, "flat", "release", false)
	legacy := expected
	legacy.Kind, legacy.Version, legacy.SourceSHA = "tenebra-legacy-snapshot", "legacy", ""
	body, err := CanonicalManifest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	_, signature, key := signatureFixture(t, "ED", body)
	files.policy.ReleasePublicKey = key
	for name, content := range map[string][]byte{"payload.json": body, "payload.sig": signature} {
		if outcome, err := input.ReplaceFile(name, content); outcome != securefs.Committed || err != nil {
			t.Fatal(outcome, err)
		}
	}
	if _, err := files.Stage(context.Background(), source, expected); err == nil {
		t.Fatal("signed operator input claimed internal legacy snapshot authority")
	}
	requireNoPublishedSource(t, files, expected)
}
