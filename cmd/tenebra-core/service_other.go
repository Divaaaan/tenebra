//go:build !windows

package main

import (
	"context"
	"errors"

	"github.com/Divaaaan/tenebra/core/control"
)

// maybeRunService is a Windows concept; on every other OS the core is always a
// plain console/sidecar process.
func maybeRunService() (handled bool, err error) { return false, nil }

// servePipe rejects --pipe off Windows: the named-pipe transport (and the
// service that uses it) exists only there.
func servePipe(context.Context, *control.Daemon) error {
	return errors.New("--pipe: the named-pipe transport is only available on Windows")
}
