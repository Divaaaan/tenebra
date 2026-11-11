//go:build !linux && !darwin

package main

import (
	"context"
	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func unixServiceRoot() bool { return false }
func openManagedFilesystem(context.Context, runAuthority) (daemonFilesystem, error) {
	return daemonFilesystem{}, securefs.ErrUnsupported
}
func startManagedPrivate(context.Context, *control.Daemon, *daemonFilesystem) (func(), error) {
	return nil, securefs.ErrUnsupported
}
func serveManagedSocket(context.Context, *control.Daemon, string) error {
	return securefs.ErrUnsupported
}
func newManagedRunner(string) control.Runner { return nil }
