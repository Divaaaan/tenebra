package installtx

import (
	"context"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// InspectRuntime is the read-only boot side of the same protected payload
// verifier. It accepts no operator input policy or manifest public-key claim.
func InspectRuntime(ctx context.Context, releases *securefs.Root, ref RuntimeRef, target string) (Manifest, error) {
	if releases == nil || !oneOf(target, "linux-amd64", "darwin-universal") {
		return Manifest{}, ErrProtocol
	}
	reader := ProtectedFS{releases: releases, privatePolicy: securefs.Policy{Mode: securefs.MachinePrivate}, policy: SourcePolicy{Target: target}}
	return reader.ReadRuntimeManifest(ctx, ref)
}
