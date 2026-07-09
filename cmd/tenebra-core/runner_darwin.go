//go:build darwin

package main

import (
	"github.com/Divaaaan/tenebra/adapters/macos"
	"github.com/Divaaaan/tenebra/core/control"
)

// newRunner builds the tunnel supervisor for macOS. macOS gets its own adapter
// (rather than reusing the generic one in runner_other.go) because opening the
// utun device requires privilege the Windows wintun path does not — see
// adapters/macos and docs/porting/macos.md.
func newRunner() control.Runner {
	return macos.New()
}
