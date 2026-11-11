package installtx

import (
	"context"
	"errors"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// The native fixed-job verifier supplies this already protected input root.
// Only the five installed runtime roles are read. Existing helper files and
// unrelated directory entries provide neither content nor build authority.
func (f *ProtectedFS) snapshotLegacyInput(ctx context.Context, input *securefs.Root) (reference *RuntimeRef, failure error) {
	f.mu.Lock()
	if f.reserved+len(f.stages) >= 32 {
		f.mu.Unlock()
		return nil, securefs.ErrLimit
	}
	f.reserved++
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.reserved--; f.mu.Unlock() }()

	copied, err := f.releases.NewStage("legacy-")
	if err != nil {
		return nil, err
	}
	defer func() { failure = errors.Join(failure, copied.Close()) }()
	names := make([]string, 0, len(payloadRoles))
	for name := range payloadRoles {
		names = append(names, name)
	}
	limits := securefs.Limits{MaxFiles: len(payloadRoles), MaxBytes: MaxExpandedBytes}
	if err := securefs.CopyFilesToStage(ctx, input, copied, names, limits); err != nil {
		return nil, err
	}
	lease, err := copied.Root().LeaseTree(ctx, limits, nil)
	if err != nil {
		return nil, err
	}
	defer func() { failure = errors.Join(failure, lease.Close()) }()
	paths := rolePaths("flat")
	files, err := manifestFilesFromLease(ctx, lease, paths)
	if err != nil {
		return nil, err
	}
	manifest := Manifest{Schema: 1, Kind: "tenebra-legacy-snapshot", Version: "legacy", Target: "linux-amd64", Files: files}
	payload, err := f.stagePayload(ctx, lease, paths, manifest, "legacy_root_owned")
	if err != nil {
		return nil, err
	}
	defer func() {
		failure = errors.Join(failure, f.RemoveOwnedStage(context.WithoutCancel(ctx), payload.StageID))
	}()
	ref, err := f.PublishRuntime(ctx, payload)
	if err != nil {
		return nil, err
	}
	return &ref, nil
}
