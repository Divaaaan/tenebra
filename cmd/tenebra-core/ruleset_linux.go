//go:build linux

package main

import (
	"os"
	"path/filepath"

	"github.com/Divaaaan/tenebra/adapters/linux"
)

// ruleSetCandidates lists the directories the bundled RU rule-sets may live in,
// in probe order. On Linux the .srs cannot be assumed to sit beside the sing-box
// binary the way they do inside a Windows install folder or a macOS app bundle:
// a distribution package puts architecture-independent data under
// <prefix>/share, and sing-box itself may be a packaged dependency in /usr/bin
// that knows nothing about Tenebra's rule-sets. So the order is:
//
//   - the directory holding TENEBRA_SINGBOX, which keeps the self-contained
//     layout (dev checkout, AppImage, /opt install) working exactly as before
//     and lets an operator override the whole search by pointing at a directory
//     that carries both;
//   - the platform's install directories (adapters/linux.InstallDirs) — the same
//     list, in the same order, the sing-box resolution walks, so the two can
//     never disagree about where a Tenebra install keeps its files.
//
// A miss here is not fatal: ruleSetDir logs everywhere it looked and the routing
// layer falls back to downloading the sets at connect time.
func ruleSetCandidates() []string {
	var out []string
	if bin := os.Getenv("TENEBRA_SINGBOX"); bin != "" {
		out = append(out, filepath.Dir(bin))
	}
	exe, err := os.Executable()
	if err != nil {
		// Without the executable's location only the override is knowable. That
		// is still a usable answer, so return it rather than give up entirely.
		return out
	}
	for _, dir := range linux.InstallDirs(filepath.Dir(exe)) {
		if len(out) > 0 && dir == out[0] {
			continue
		}
		out = append(out, dir)
	}
	return out
}
