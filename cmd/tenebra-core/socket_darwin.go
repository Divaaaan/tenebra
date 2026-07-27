//go:build darwin

package main

import "path/filepath"

// rootDataDir is the machine-scoped profile store for the root LaunchDaemon —
// the darwin analog of the Windows service's %ProgramData%\Tenebra\data. It sits
// under /Library/Application Support (the macOS home for machine-wide app data)
// and is locked to root by configureSocketPaths, since profiles carry
// subscription credentials that unprivileged users reach through the socket
// protocol, never the files.
const rootDataDir = "/Library/Application Support/Tenebra/data"

// singboxBinaryName is the bundled sing-box filename on macOS: no extension,
// unlike the Windows sing-box.exe (matching adapters/macos's own resolution).
const singboxBinaryName = "sing-box"

// singboxCandidates lists where sing-box may sit in an installed macOS layout,
// in the order findSingbox probes them. The app bundles sing-box as a Tauri
// externalBin sidecar, which lands next to the main binary (Contents/MacOS/), as
// does a flat dev checkout; a bundle that instead stages it under
// Contents/Resources is covered by the sibling fallback. Unlike Linux, there is
// no PATH lookup: a macOS install is a self-contained bundle, and picking up a
// stray Homebrew sing-box instead of the pinned one would be a silent version
// mismatch, not a feature.
func singboxCandidates(exeDir string) []string {
	return []string{
		filepath.Join(exeDir, singboxBinaryName),
		filepath.Join(exeDir, "..", "Resources", singboxBinaryName),
	}
}
