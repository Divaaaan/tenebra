// Package tenebracore is Tenebra's config generator, shared by the desktop core
// and the mobile clients. It is a *pure generator*: it turns a profile plus routing
// and tun options into a sing-box configuration JSON string and never imports
// sing-box itself, exactly like the desktop core. The generator and the sing-box
// engine meet only at that JSON string — the app hands it to libbox's
// CommandServer.StartOrReloadService — which is the generator-vs-engine split the
// whole project is built on. Do NOT add a sing-box import to this package.
//
// # How it reaches the phone
//
// On mobile the generator is NOT gomobile-bound on its own. A separate wrapper
// module (../mobile) imports this package AND sing-box's experimental/libbox and
// binds BOTH in a single gomobile pass into ONE artifact — a single tenebra.aar on
// Android, a single Tenebra.xcframework on iOS. That is mandatory, not a
// convenience: two independent gomobile artifacts cannot share one process. Each
// carries its own Go runtime and its own copy of the gomobile support package `go`
// (go.Seq, go.Universe), so linking a standalone core .aar next to a standalone
// libbox.aar makes Android's D8 fail on duplicate go/Seq and go/Universe classes
// and, even past that, would load two Go runtimes. Binding the wrapper and libbox
// together yields one runtime and one `go` package, with the generated classes
// (Tenebracore and Libbox*) side by side. See ../mobile,
// scripts/build-libbox-android.sh and scripts/build-libbox.sh.
//
// # Layout
//
// The exported surface — GenerateConfig, ImportSubscription, OrderNodes, Version,
// all string-in / string-or-error-out — lives in this package as ordinary exported
// functions with NO build tag, so the desktop and CI builds compile and unit-test
// them on every host, including Windows/Linux CI where no mobile toolchain exists.
// The ../mobile wrapper re-exports them from a package also named tenebracore, so
// gomobile renders them as Tenebracore.generateConfig(...) from Kotlin and
// TenebracoreGenerateConfig(...) from Swift. This package carries no gomobile
// specifics of its own; it is a plain library the wrapper binds.
//
// SCAFFOLD STATUS — the Go here is unit-tested on the desktop, but the gomobile
// *bind* in ../mobile that turns it (with libbox) into a mobile artifact has only
// been exercised on CI for Android; iOS needs a Mac. See ui-android/README.md,
// ui-ios/README.md and the build scripts.
package tenebracore

import (
	"encoding/json"
	"fmt"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
)

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

// GenerateConfig turns a profile (plus optional routing/tun options) into a
// sing-box configuration JSON string, ready to hand to libbox unchanged. It mirrors
// the desktop daemon's per-connect config build exactly: extract the profile's
// nodes, construct routing/tun options, and delegate to singbox.Build, which
// normalizes and validates. On mobile, set tun.externalTun so the tun inbound omits
// auto_route — the platform owns routing. Errors (no usable nodes, invalid routing)
// come back as a Go error, which gomobile surfaces to the host as a thrown
// exception/NSError.
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

// bridgeVersion is the ABI version of this binding — the shape of the
// request/response envelopes, NOT the app or sing-box version. Bump it when an
// envelope changes so the native side can detect a mismatch.
const bridgeVersion = "0.2.0"

// Version returns the binding's ABI version — the shape of these calls'
// request/response envelopes, distinct from the sing-box engine and app versions —
// so a mismatched host and binding can be detected.
func Version() string {
	return bridgeVersion
}
