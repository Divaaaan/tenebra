//go:build !darwin && !linux

package main

import (
	"github.com/Divaaaan/tenebra/adapters/windows"
	"github.com/Divaaaan/tenebra/core/control"
)

// newRunner builds the tunnel supervisor for Windows, the shipped desktop target
// this adapter is named for, and for any other platform the core still compiles
// on: the windows adapter uses nothing OS-specific at the source level and only
// touches wintun when the OS is actually Windows. macOS and Linux, which need
// their own privilege notes and diagnostics, override this in runner_darwin.go
// and runner_linux.go.
func newRunner() control.Runner {
	return windows.New()
}
