//go:build tools

// This file exists only to keep sagernet/gomobile in the module graph. gomobile
// bind generates a small Go program that imports gomobile/bind to emit the
// JNI/Objective-C wrappers, and that import must resolve against this module's
// go.mod when binding ./core-bridge into the .aar/.xcframework. Without a
// reference here, `go mod tidy` would drop the dependency and the bind would fail
// with "no Go package in github.com/sagernet/gomobile/bind".
//
// The `tools` build tag is set by no normal build, so this never compiles into
// the desktop binary or the mobile runtime — it only pins the version for the
// build-time bind step. Keep it in sync with the gomobile fork sing-box's Makefile
// installs for the pinned tag (scripts/build-libbox-android.sh).
package tenebracore

import _ "github.com/sagernet/gomobile/bind"
