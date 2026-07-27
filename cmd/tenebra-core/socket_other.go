//go:build !darwin && !linux

package main

import (
	"context"
	"errors"

	"github.com/Divaaaan/tenebra/core/control"
)

// serveSocket rejects --socket on platforms without the unix-socket transport.
// It is the counterpart of Windows' named pipe and service, and exists where a
// privileged daemon can serve the protocol on a filesystem path — macOS and
// Linux — the way servePipe rejects --pipe everywhere but Windows.
func serveSocket(context.Context, *control.Daemon) error {
	return errors.New("--socket: the unix-socket transport is only available on macOS and Linux")
}

// configureSocketPaths has nothing to prepare on platforms where serveSocket
// rejects the socket transport outright.
func configureSocketPaths() error { return nil }
