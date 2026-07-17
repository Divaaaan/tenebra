//go:build tools

// This file pins the modules the gomobile bind needs at build time but that no
// normal compile imports, so `go mod tidy` keeps them in mobile/go.mod instead of
// pruning them as unused:
//
//   - gomobile/bind: gomobile bind generates a small Go program that imports it to
//     emit the JNI / Objective-C wrappers, and that import must resolve against
//     mobile/go.mod. Without it the bind fails with "no Go package in
//     github.com/sagernet/gomobile/bind".
//   - experimental/libbox: the sing-box engine this wrapper is bound *together*
//     with — scripts/build-libbox-android.sh passes it as a second package to one
//     `gomobile bind`. Referencing it here pulls sing-box and its whole tag-gated
//     graph (gVisor, quic-go, utls, wireguard, tailscale) into go.sum, so the
//     tagged bind can compile it. The wrapper itself never imports libbox: the
//     generator stays pure (see mobile.go and ../core-bridge).
//
// The `tools` tag is set by no normal build, so nothing here compiles into the
// mobile runtime — it only shapes the module graph. Keep the gomobile version in
// go.mod in sync with the fork sing-box's `make lib_install` installs (v0.1.12 for
// sing-box v1.13.14); the wrapper and libbox must be bound with the same gomobile.
package tenebracore

import (
	_ "github.com/sagernet/gomobile/bind"
	_ "github.com/sagernet/sing-box/experimental/libbox"
)
