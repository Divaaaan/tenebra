//go:build !darwin

package main

import (
	"context"
	"errors"

	"github.com/Divaaaan/tenebra/core/control"
)

// serveSocket rejects --socket off macOS: the unix-socket transport, and the
// root LaunchDaemon that uses it, are the macOS analog of Windows' named pipe
// and service — they exist only there, the way servePipe rejects --pipe
// everywhere but Windows.
func serveSocket(context.Context, *control.Daemon) error {
	return errors.New("--socket: the unix-socket transport is only available on macOS")
}

// configureSocketPaths has nothing to prepare off macOS, where serveSocket
// rejects the socket transport outright.
func configureSocketPaths() error { return nil }
