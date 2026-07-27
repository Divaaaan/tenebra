// Package buildinfo carries the release version the core was built from.
//
// The daemon reports it in every State snapshot (see core/control), which is
// what lets a GUI detect a stale privileged daemon: on Windows the installer
// replaces the service together with the app, but on macOS the in-app updater
// refreshes only the .app bundle — the hand-installed LaunchDaemon keeps
// running the binaries from whenever install-daemon.sh last ran, and every
// command added since then dies with "unknown command" while the UI's toggles
// silently do nothing. Comparing this constant against the app's own version
// turns that silent skew into a visible warning.
//
// The literal is rewritten by scripts/set-version.mjs alongside the package
// manifest, the Tauri config and the crate manifest, so a release can never
// ship with the copies out of sync. Do not edit it by hand.
package buildinfo

// Version is the semantic version of this build, kept in sync with the
// desktop app's version by scripts/set-version.mjs.
const Version = "0.4.6"
