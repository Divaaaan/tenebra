// Package tenebracore is the mobile bind surface: the single gomobile artifact the
// Android and iOS clients link. It re-exports Tenebra's pure config generator
// (../core-bridge) as thin delegates, and is bound in one gomobile pass together
// with sing-box's experimental/libbox — so the phone loads one Go runtime and one
// gomobile support package `go`, with the Tenebracore and Libbox* classes side by
// side. Binding the generator on its own would collide with libbox's own runtime
// and duplicate go/Seq + go/Universe; see ../core-bridge for the full rationale.
//
// The package is named tenebracore (not mobile, the directory) on purpose: gomobile
// names the generated class after the package, so this is what makes the class
// Tenebracore — io.nekohasekai.tenebracore.Tenebracore on Android and the
// TenebracoreGenerateConfig(...) free functions on Swift. See
// scripts/build-libbox-android.sh and scripts/build-libbox.sh.
//
// There is deliberately no build tag here: this is a gomobile bind entrypoint, and
// gomobile sets the GOOS (android/ios) tag itself. The delegates carry no logic of
// their own; the logic and its host-runnable tests live in ../core-bridge.
package tenebracore

import core "github.com/Divaaaan/tenebra/core-bridge"

// GenerateConfig turns a profile JSON envelope into a sing-box config JSON string.
func GenerateConfig(requestJSON string) (string, error) {
	return core.GenerateConfig(requestJSON)
}

// ImportSubscription fetches a subscription URL and returns the profile JSON.
func ImportSubscription(requestJSON string) (string, error) {
	return core.ImportSubscription(requestJSON)
}

// OrderNodes returns the profile's selector tags in anti-DPI fallback order.
func OrderNodes(requestJSON string) (string, error) {
	return core.OrderNodes(requestJSON)
}

// NodeTags returns the profile's stable server ID -> selector tag mapping, for
// switching the live tunnel to a specific node without a rebuild.
func NodeTags(requestJSON string) (string, error) {
	return core.NodeTags(requestJSON)
}

// Version returns the binding's ABI version.
func Version() string {
	return core.Version()
}
