//go:build !darwin

package main

import (
	"github.com/Divaaaan/tenebra/adapters/windows"
	"github.com/Divaaaan/tenebra/core/control"
)

// newRunner builds the tunnel supervisor for every non-macOS target the core
// builds for. The windows adapter is the generic sidecar supervisor — it only
// touches wintun when the OS is actually Windows — so it backs Windows (the
// shipped desktop target) as well as the Linux build the CI compiles and tests.
// macOS overrides this in runner_darwin.go.
func newRunner() control.Runner {
	return windows.New()
}
