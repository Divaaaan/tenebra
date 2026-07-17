// Package tenebracore is the shared gomobile binding that exposes Tenebra's
// config generator to the mobile clients. One package, two artifacts: gomobile
// targets GOOS=ios to produce a `.xcframework` and GOOS=android to produce an
// `.aar`. Either way the generator is compiled in-process — there is no
// desktop-style sidecar process on mobile — and called from the host app (Swift
// on iOS, Kotlin on Android; see docs/porting/ios.md).
//
// # Generator, not engine
//
// This bridge deliberately stays a *pure config generator* with no sing-box
// import, exactly like the desktop core. It is gomobile-bound into its own small
// artifact (`TenebraCore.xcframework` on iOS, the equivalent `.aar` on Android),
// while sing-box's engine ships as a separate, unmodified libbox artifact. The two
// meet only at a JSON string: the app calls GenerateConfig here, then hands the
// result to libbox's `CommandServer.StartOrReloadService(json)`. Keeping them
// apart preserves the generator-vs-engine split the whole project is built on. Do
// not add a sing-box dependency to this package.
//
// # Layout and the build tag
//
// The exported, gomobile-representable surface (string in, string/error out) lives
// in bridge.go under a `//go:build ios || android` constraint, so gomobile picks
// it up for both mobile targets while the desktop and CI builds — which set
// neither tag — exclude it and can never be broken by it. The actual logic lives
// in build-tag-free files (this one, import.go, order.go) as ordinary functions,
// so it compiles and its tests run on every host, including Windows/Linux CI where
// no mobile toolchain exists. bridge.go is a thin wrapper that delegates to them.
//
// SCAFFOLD STATUS — the Go here cross-compiles for both mobile targets and is unit
// tested on the desktop, but the gomobile *bind* that turns it into a mobile
// artifact has NOT been exercised on the host this was written on (the iOS bind
// needs macOS + Xcode, the Android bind needs the NDK). See ui-ios/README.md and
// scripts/build-libbox.sh.
package tenebracore

import (
	"encoding/json"
	"fmt"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// bridgeVersion is the ABI version of this binding — the shape of the
// request/response envelopes below, NOT the app or sing-box version. Bump it when
// an envelope changes so the native side can detect a mismatch.
const bridgeVersion = "0.2.0"

// generateRequest is the JSON envelope generateConfig accepts. It mirrors the
// inputs the desktop daemon feeds singbox.Build for one connect (a profile's
// nodes plus routing and tun options), so the mobile config path stays a faithful
// re-homing of the desktop one rather than a second implementation.
type generateRequest struct {
	// Profile is a full profile as persisted by the core (see
	// docs/control-protocol.md#profile). Only its nodes are read here.
	Profile profile.Profile `json:"profile"`
	// SelectedTag is the sing-box selector tag to default to. Empty lets
	// singbox.Build pick the first usable node. Runtime node switching on mobile is
	// expected to go through libbox's CommandClient.SelectOutbound, not a rebuild.
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
	// RuleSetDir points at the directory where the HOST app has cached the binary
	// .srs rule-sets. On mobile the memory-constrained tunnel process must read
	// local rule-sets, never fetch them (see docs/porting/ios.md#memory-budget).
	RuleSetDir string `json:"ruleSetDir,omitempty"`
}

// tunOptions is the gomobile-facing projection of singbox.TunOptions. On mobile
// the tun device is supplied by the platform VPN API, not opened from this config;
// these fields still shape the tun inbound sing-box builds.
type tunOptions struct {
	InterfaceName string `json:"interfaceName,omitempty"`
	MTU           int    `json:"mtu,omitempty"`
	Stack         string `json:"stack,omitempty"` // system | gvisor | mixed
	// ExternalTun marks the tun as owned by the platform VPN API rather than opened
	// and routed by sing-box. On mobile it must be true: the fd comes from the OS
	// (Android VpnService, iOS NEPacketTunnelProvider) and the OS holds the routes,
	// so the generated tun inbound omits auto_route/strict_route to avoid fighting
	// the platform (see singbox.TunOptions.ExternalTun). MTU and Stack still apply.
	ExternalTun bool `json:"externalTun,omitempty"`
}

// generateConfig turns a profile (plus optional routing/tun options) into a
// sing-box configuration JSON string. It mirrors the desktop daemon's per-connect
// config build exactly: extract the profile's nodes, construct routing/tun
// options, and delegate to singbox.Build, which normalizes and validates. Errors
// (no usable nodes, invalid routing) come back as a Go error, which the gomobile
// wrapper surfaces to the host as a thrown exception/NSError.
func generateConfig(requestJSON string) (string, error) {
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
			ExternalTun:   req.Tun.ExternalTun,
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
