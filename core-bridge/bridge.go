//go:build ios || android

// Package tenebracore is the shared gomobile binding that exposes Tenebra's
// config generator to the mobile clients. One package, two artifacts: gomobile
// targets GOOS=ios to produce a `.xcframework` and GOOS=android to produce an
// `.aar`. Either way the generator is compiled in-process — there is no
// desktop-style sidecar process on mobile — and called from the host app (Swift
// on iOS, Kotlin on Android; see docs/porting/ios.md).
//
// SCAFFOLD STATUS — this file is portable Go and cross-compiles for both mobile
// targets, but the gomobile *bind* that turns it into a mobile artifact has NOT
// been exercised on the host this was written on: the iOS `.xcframework` bind
// needs macOS + Xcode, the Android `.aar` bind needs the NDK. See ui-ios/README.md
// and scripts/build-libbox.sh. The Go here is real and tested for compilation
// (`GOOS=android GOARCH=arm64 go build ./core-bridge`, plus the darwin/ios
// equivalent); the frameworks it becomes are not.
//
// # Why a separate module boundary
//
// This bridge deliberately stays a *pure config generator* with no sing-box
// import, exactly like the desktop core. It is gomobile-bound into its own small
// artifact (`TenebraCore.xcframework` on iOS, the equivalent `.aar` on Android),
// while sing-box's engine ships as a separate, unmodified libbox artifact. The
// two meet only at a JSON string: the app calls GenerateConfig here, then hands
// the result to libbox's `CommandServer.StartOrReloadService(json)`. Keeping them
// apart preserves the generator-vs-engine split the whole project is built on. Do
// not add a sing-box dependency to this package.
//
// # The build tag
//
// The `ios || android` constraint keeps this file out of every desktop and CI
// build (`go build ./...` on windows/linux/darwin sets neither tag, so the file is
// excluded), which is what lets it never break the main build. gomobile sets the
// tag automatically: GOOS=ios for the Apple bind (tag `ios`), GOOS=android for the
// Android bind (tag `android`). To compile-check on a host without a mobile
// toolchain, cross-build for either target — `GOOS=android GOARCH=arm64 go build
// ./core-bridge`, or `GOOS=darwin GOARCH=arm64 go build -tags ios ./core-bridge`.
//
// # gomobile surface
//
// Only gomobile-representable signatures are exported (string/error in and out).
// gomobile renders package-level functions as free functions prefixed with the
// capitalized package name, so `GenerateConfig` is called from Swift roughly as
// `TenebracoreGenerateConfig(requestJSON, &error)` and from Kotlin as
// `Tenebracore.generateConfig(requestJSON)`. The exact spelling depends on the
// gomobile fork/version and the `-o` artifact name; confirm against the generated
// headers/bindings.
package tenebracore

import (
	"encoding/json"
	"fmt"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// bridgeVersion is the ABI version of this bridge — the shape of the
// GenerateConfig request/response, NOT the app or sing-box version. Bump it when
// the request envelope below changes so the Swift side can detect a mismatch.
const bridgeVersion = "0.1.0"

// generateRequest is the JSON envelope GenerateConfig accepts. It mirrors the
// inputs the desktop daemon feeds singbox.Build for one connect (a profile's
// nodes plus the routing and tun options), so the iOS config path stays a
// faithful re-homing of the desktop one rather than a second implementation.
type generateRequest struct {
	// Profile is a full profile as persisted by the core (see
	// docs/control-protocol.md#profile). Only its nodes are read here.
	Profile profile.Profile `json:"profile"`
	// SelectedTag is the sing-box selector tag to default to. Empty lets
	// singbox.Build pick the first usable node, matching desktop behaviour when no
	// explicit exit is chosen. Runtime node switching on iOS is expected to go
	// through libbox's CommandClient.SelectOutbound rather than a config rebuild.
	SelectedTag string `json:"selectedTag,omitempty"`
	// Routing and Tun are optional; a nil value means "defaults", which
	// singbox.Build fills via routing.Options.Normalize / TunOptions.normalize.
	Routing *routingOptions `json:"routing,omitempty"`
	Tun     *tunOptions     `json:"tun,omitempty"`
}

// routingOptions is the gomobile-facing projection of routing.Options. It carries
// only JSON-friendly scalars; unset fields fall back to the core's defaults.
type routingOptions struct {
	Mode       string `json:"mode,omitempty"` // smart | global | direct
	BypassLAN  bool   `json:"bypassLan,omitempty"`
	IPv4Only   bool   `json:"ipv4Only,omitempty"`
	KillSwitch bool   `json:"killSwitch,omitempty"`
	DNSRemote  string `json:"dnsRemote,omitempty"`
	DNSDirect  string `json:"dnsDirect,omitempty"`
	// RuleSetDir points at the App Group directory where the HOST app has cached
	// the binary .srs rule-sets. On iOS the memory-constrained extension must read
	// local rule-sets, never fetch them (see docs/porting/ios.md#memory-budget).
	RuleSetDir string `json:"ruleSetDir,omitempty"`
}

// tunOptions is the gomobile-facing projection of singbox.TunOptions. Note that
// on iOS the tun device is supplied by the Network Extension's packetFlow, not
// opened from this config; these fields still shape the tun inbound sing-box
// builds (stack, MTU) and so remain meaningful.
type tunOptions struct {
	InterfaceName string `json:"interfaceName,omitempty"`
	MTU           int    `json:"mtu,omitempty"`
	Stack         string `json:"stack,omitempty"` // system | gvisor | mixed
}

// GenerateConfig turns a profile (plus optional routing/tun options) into a
// sing-box configuration JSON string. It is the single call the host app needs
// from Tenebra's core; the returned JSON is handed to libbox unchanged.
//
// It mirrors the desktop daemon's per-connect config build exactly: extract the
// profile's nodes, construct routing/tun options, and delegate to singbox.Build,
// which normalizes and validates. Errors (no usable nodes, invalid routing) are
// returned as a Go error, which gomobile surfaces to Swift as a thrown NSError.
func GenerateConfig(requestJSON string) (string, error) {
	var req generateRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("tenebracore: decode request: %w", err)
	}

	// Servers -> nodes, preserving order (mirrors control.profileNodes).
	nodes := make([]model.Node, len(req.Profile.Servers))
	for i, s := range req.Profile.Servers {
		nodes[i] = s.Node
	}

	var ro routing.Options
	if req.Routing != nil {
		ro = routing.Options{
			Mode:       routing.Mode(req.Routing.Mode),
			BypassLAN:  req.Routing.BypassLAN,
			IPv4Only:   req.Routing.IPv4Only,
			KillSwitch: req.Routing.KillSwitch,
			DNSRemote:  req.Routing.DNSRemote,
			DNSDirect:  req.Routing.DNSDirect,
			RuleSetDir: req.Routing.RuleSetDir,
		}
	}

	var tun singbox.TunOptions
	if req.Tun != nil {
		tun = singbox.TunOptions{
			InterfaceName: req.Tun.InterfaceName,
			MTU:           req.Tun.MTU,
			Stack:         req.Tun.Stack,
		}
	}

	// singbox.Build normalizes and validates ro/tun internally, so a zero-value
	// Options is a valid "use defaults" request.
	cfg, err := singbox.Build(nodes, req.SelectedTag, ro, tun)
	if err != nil {
		return "", fmt.Errorf("tenebracore: build config: %w", err)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("tenebracore: encode config: %w", err)
	}
	return string(out), nil
}

// Version returns the bridge ABI version. It is intentionally distinct from the
// sing-box engine version (which libbox reports via its own Version()) and from
// the app's marketing version; it identifies the request/response contract of
// this bridge so a mismatched host and framework can be detected.
func Version() string {
	return bridgeVersion
}
