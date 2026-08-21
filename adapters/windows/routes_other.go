//go:build !windows

package windows

import "github.com/Divaaaan/tenebra/core/tunguard"

// Interfaces has no implementation off Windows.
//
// It reports "nothing known" rather than an error on purpose: the caller treats
// an empty interface list as "no conflict detected", which is the correct
// behaviour for a platform whose route table this adapter cannot read. Returning
// an error instead would surface a scary failure on macOS/Linux for a check that
// simply is not wired up there yet — the platform adapters own their own
// enumeration, and each will supply it when implemented.
//
// This file exists so the package keeps compiling and vetting on every platform
// the CI builds, matching how core/control splits its own OS-specific pieces
// (ping_dial_windows.go / ping_dial_other.go).
func Interfaces() ([]tunguard.Iface, error) { return nil, nil }
