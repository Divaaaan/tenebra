package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// Console/sidecar development deliberately uses effective-account trust.
// A service never chooses this policy from an environment value or pathname.
func openDevelopmentFilesystem(ctx context.Context) (daemonFilesystem, error) {
	if err := ctx.Err(); err != nil {
		return daemonFilesystem{}, err
	}
	policy := securefs.Policy{Mode: securefs.SameAccountPrivate}
	dir := configDir()
	state, err := securefs.OpenRoot(dir, policy, true)
	if err != nil {
		return daemonFilesystem{}, err
	}
	private, err := securefs.OpenRoot(filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-private"), policy, true)
	if err != nil {
		return daemonFilesystem{}, errors.Join(err, state.Close())
	}
	return daemonFilesystem{State: state, Private: private, Development: true, privatePolicy: policy, closers: []io.Closer{state, private}}, nil
}
