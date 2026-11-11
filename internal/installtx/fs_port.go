package installtx

import (
	"context"
	"errors"
	"io/fs"
	"sync"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// SourcePolicy is trusted composition-root policy, never decoded from a helper
// request or source manifest. In particular the vendor public key and the
// authenticated operator principal cannot be supplied by package contents.
type SourcePolicy struct {
	Trust, OperatorPrincipal, ReleasePublicKey, HelperSHA256, Target string
	BoundPayloadSHA256, BoundArchiveSHA256                           string
	AcknowledgeUntrusted                                             bool
}
type stagedRuntime struct {
	stage   *securefs.Stage
	payload StagedPayload
}
type ProtectedFS struct {
	*JournalFile
	releases      *securefs.Root
	policy        SourcePolicy
	privatePolicy securefs.Policy
	mu            sync.Mutex
	stages        map[string]stagedRuntime
	reserved      int
	legacy        func(context.Context) (*RuntimeRef, error)
}

// Only native composition can provide this fixed-job snapshot capability.
// Neither the source manifest nor request JSON can request legacy provenance.
func (f *ProtectedFS) SnapshotLegacy(ctx context.Context) (*RuntimeRef, error) {
	if f.legacy == nil {
		return nil, nil
	}
	return f.legacy(ctx)
}

// The composition root verified this running helper identity; request JSON has
// no helper-digest field. Package hooks use it when refreshing a pending grant.
func (f *ProtectedFS) HelperIdentity() string { return f.policy.HelperSHA256 }

func NewProtectedFS(releases *securefs.Root, journal *JournalFile, policy SourcePolicy) (*ProtectedFS, error) {
	return newProtectedFS(releases, journal, policy, securefs.Policy{Mode: securefs.MachinePrivate})
}
func newProtectedFS(releases *securefs.Root, journal *JournalFile, policy SourcePolicy, private securefs.Policy) (*ProtectedFS, error) {
	if releases == nil || journal == nil || policy.OperatorPrincipal == "" || !hexValue(policy.HelperSHA256, 64) ||
		!oneOf(policy.Target, "linux-amd64", "darwin-universal") || !oneOf(policy.Trust, "release", "package_manager", "operator_dev") ||
		!oneOfPolicy(private.Mode) {
		return nil, ErrProtocol
	}
	if policy.BoundPayloadSHA256 != "" || policy.BoundArchiveSHA256 != "" {
		if policy.Trust != "release" || policy.Target != "darwin-universal" ||
			!hexValue(policy.BoundPayloadSHA256, 64) || !hexValue(policy.BoundArchiveSHA256, 64) {
			return nil, ErrProtocol
		}
	}
	if policy.Trust == "release" {
		if _, err := minisignPublicKey(policy.ReleasePublicKey); err != nil {
			return nil, ErrSignature
		}
	}
	return &ProtectedFS{JournalFile: journal, releases: releases, policy: policy, privatePolicy: private, stages: map[string]stagedRuntime{}}, nil
}
func oneOfPolicy(mode securefs.Mode) bool {
	return mode == securefs.MachinePrivate || mode == securefs.SameAccountPrivate
}
func (f *ProtectedFS) PublishRuntime(ctx context.Context, payload StagedPayload) (RuntimeRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned, ok := f.stages[payload.StageID]
	if !ok || owned.payload.Runtime != payload.Runtime {
		return RuntimeRef{}, ErrProtocol
	}
	wanted, err := CanonicalManifest(owned.payload.Manifest)
	if err != nil {
		return RuntimeRef{}, err
	}
	supplied, err := CanonicalManifest(payload.Manifest)
	if err != nil || string(wanted) != string(supplied) {
		return RuntimeRef{}, ErrProtocol
	}
	verify := func(ctx context.Context, lease *securefs.PathLease) error {
		return verifyRuntimeLease(ctx, lease, owned.payload.Manifest)
	}
	root, outcome, err := owned.stage.Publish(ctx, payload.Runtime.ArtifactSHA256, securefs.Limits{MaxFiles: MaxSelectedRoles + 1, MaxBytes: MaxExpandedBytes + MaxMetadataBytes}, verify)
	if errors.Is(err, fs.ErrExist) {
		existing, openErr := f.releases.OpenDir(payload.Runtime.ArtifactSHA256, f.privatePolicy, false)
		if openErr != nil {
			return RuntimeRef{}, openErr
		}
		defer existing.Close()
		lease, check := existing.LeaseTree(ctx, securefs.Limits{MaxFiles: MaxSelectedRoles + 1, MaxBytes: MaxExpandedBytes + MaxMetadataBytes}, verify)
		if check != nil {
			return RuntimeRef{}, check
		}
		if err := lease.Close(); err != nil {
			return RuntimeRef{}, err
		}
		return owned.payload.Runtime, nil
	}
	if err != nil || outcome != securefs.Committed {
		if outcome != securefs.Unchanged {
			return RuntimeRef{}, errors.Join(ErrRecoveryRequired, err)
		}
		if err == nil {
			err = ErrProtocol
		}
		return RuntimeRef{}, err
	}
	if err := root.Close(); err != nil {
		return RuntimeRef{}, errors.Join(ErrRecoveryRequired, err)
	}
	return owned.payload.Runtime, nil
}
func (f *ProtectedFS) RemoveOwnedStage(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	stage, ok := f.stages[id]
	if !ok {
		return fs.ErrNotExist
	}
	if err := stage.stage.Close(); err != nil {
		return err
	}
	delete(f.stages, id)
	return nil
}
func (f *ProtectedFS) ReadRuntimeManifest(ctx context.Context, runtime RuntimeRef) (Manifest, error) {
	if !validRuntime(&runtime) {
		return Manifest{}, ErrProtocol
	}
	root, err := f.releases.OpenDir(runtime.ArtifactSHA256, f.privatePolicy, false)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	lease, err := root.LeaseTree(ctx, securefs.Limits{MaxFiles: MaxSelectedRoles + 1, MaxBytes: MaxExpandedBytes + MaxMetadataBytes}, nil)
	if err != nil {
		return Manifest{}, err
	}
	defer lease.Close()
	data, err := lease.ReadFile("payload.json", MaxMetadataBytes)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err := DecodeManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	if (runtime.Provenance == "legacy_root_owned") != (manifest.Kind == "tenebra-legacy-snapshot") {
		return Manifest{}, ErrProtocol
	}
	hash, err := artifactHash(manifest)
	if err != nil || hash != runtime.ArtifactSHA256 || manifest.Version != runtime.Version || manifest.Target != f.policy.Target {
		return Manifest{}, ErrProtocol
	}
	if err := verifyRuntimeLease(ctx, lease, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
