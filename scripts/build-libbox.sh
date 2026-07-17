#!/usr/bin/env bash
# Builds the single gomobile xcframework the iOS client links (see
# docs/porting/ios.md and ui-ios/README.md):
#
#   Tenebra.xcframework — ONE framework carrying BOTH Tenebra's config generator
#                         (the ../mobile wrapper over core-bridge) AND sing-box's
#                         experimental/libbox engine, bound together in one pass.
#
# Why one framework and not two: two independent gomobile artifacts each bundle
# their own Go runtime and their own copy of the gomobile support package `go`, and
# cannot share a process. Binding the wrapper and libbox in a single pass yields one
# runtime and one `go` package, with the Tenebracore* and Libbox* symbols in one
# module. This is the Apple mirror of the Android duplicate-class problem the fused
# tenebra.aar solves (see scripts/build-libbox-android.sh).
#
# It is dropped into ui-ios/Frameworks/ for XcodeGen (project.yml) to reference.
#
# ============================================================================
#  macOS + Xcode + Go >= 1.24.7 REQUIRED. This is NOT runnable on this host.
# ============================================================================
# gomobile's Apple bind shells out to Xcode/clang for the Objective-C bridge (cgo
# is inherently on), so the Apple framework cannot be produced on Windows or Linux —
# only on a Mac with the full Xcode toolchain. This script was authored on Windows
# and has NOT been executed; the commands are transcribed from the pinned sing-box
# tag's own build entrypoint and the porting research, and must be validated on a
# real Mac. Treat it as an executable plan, not a proven build.
#
# Prerequisites on the Mac:
#   - Xcode + command line tools (xcode-select --install), an iOS SDK present.
#   - Go >= 1.24.7 (the pinned sing-box tag requires it when libbox is linked).
#   - This repo checked out; run from anywhere (paths are resolved from the script).
set -euo pipefail

# --- Pinned versions -------------------------------------------------------
# Keep sing-box in sync with scripts/fetch-resources.sh / .ps1 ($singbox_version),
# scripts/build-libbox-android.sh AND mobile/go.mod. The bind compiles libbox from
# the module graph (mobile/go.mod), so that pin decides the engine version; this
# value is used only to install the matching gomobile fork.
singbox_version="1.13.13"
# The gomobile version is NOT hardcoded here. sing-box's Makefile at the pinned tag
# installs a specific SagerNet gomobile fork (NOT upstream golang.org/x/mobile,
# which repeatedly breaks on new Xcode releases), and that same fork — also required
# by mobile/go.mod — has to build this framework, so the version is read straight
# from the tag's Makefile (see singbox_gomobile_version) rather than pinned again.

# iOS deployment floor — must match project.yml (NE gets the 50 MB cap on iOS 15+).
ios_min="15.0"

# libbox build tags — replicated EXACTLY from sing-box's own build entrypoint so the
# fused engine is bit-for-bit upstream. Source: cmd/internal/build_libbox/main.go at
# tag v$singbox_version, buildApple(): tags = sharedTags + darwinTags. A MISSING tag
# silently drops a protocol from the runtime, so do not trim this by hand. The
# separate with_low_memory tag is applied to the non-macOS platforms via the fork's
# -tags-not-macos flag below, exactly as upstream does (the NE runs under a tight
# memory cap). Our wrapper is pure Go and needs none of these tags; listing them is
# harmless to it.
libbox_tags="with_gvisor,with_quic,with_wireguard,with_utls,with_naive_outbound,with_clash_api,badlinkname,tfogo_checklinkname0,with_tailscale,ts_omit_logtail,ts_omit_ssh,ts_omit_drive,ts_omit_taildrop,ts_omit_webclient,ts_omit_doctor,ts_omit_capture,ts_omit_kube,ts_omit_aws,ts_omit_synology,ts_omit_bird,with_dhcp,grpcnotrace"

# Linker flags, from that file's sharedFlags. -checklinkname=0 is load-bearing: it
# pairs with the badlinkname / tfogo_checklinkname0 tags and lets sing-box and
# tfo-go keep their //go:linkname hacks, which Go >= 1.23 would otherwise reject at
# link time.
libbox_ldflags="-X github.com/sagernet/sing-box/constant.Version=v$singbox_version -X internal/godebug.defaultGODEBUG=multipathtcp=0 -s -w -buildid= -checklinkname=0"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$script_dir")"
frameworks_dir="$root/ui-ios/Frameworks"
mkdir -p "$frameworks_dir"

# --- Toolchain guard -------------------------------------------------------
require_macos() {
  if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "error: this script builds an Apple xcframework and only runs on macOS." >&2
    echo "       (host is $(uname -s); see the header of this file.)" >&2
    exit 1
  fi
  command -v go >/dev/null    || { echo "error: Go >= 1.24.7 not found" >&2; exit 1; }
  command -v xcodebuild >/dev/null || { echo "error: Xcode not found" >&2; exit 1; }
  command -v curl >/dev/null  || { echo "error: curl not found (needed to resolve the gomobile version from sing-box's Makefile)" >&2; exit 1; }
}

# singbox_gomobile_version reads the SagerNet gomobile fork version the pinned
# sing-box tag installs in its `lib_install` target, straight from that tag's
# Makefile, so this script never carries a second gomobile pin that could drift from
# the one mobile/go.mod requires. The tag is immutable, so this resolves to a fixed
# version per pinned singbox_version.
singbox_gomobile_version() {
  local makefile version
  makefile="$(curl -fsSL "https://raw.githubusercontent.com/SagerNet/sing-box/v$singbox_version/Makefile")" \
    || { echo "error: could not fetch sing-box v$singbox_version Makefile to resolve the gomobile version" >&2; exit 1; }
  version="$(printf '%s\n' "$makefile" \
    | sed -n 's|.*sagernet/gomobile/cmd/gomobile@\(v[0-9][0-9.]*\).*|\1|p' | head -n1)"
  if [ -z "$version" ]; then
    echo "error: could not parse the gomobile version from sing-box v$singbox_version Makefile" >&2
    exit 1
  fi
  printf '%s\n' "$version"
}

# install_gomobile installs the SagerNet gomobile fork + gobind at whatever version
# the pinned sing-box tag installs, and initializes it — the same thing sing-box's
# `make lib_install` does, but resolved from the tag's Makefile rather than a
# hardcoded copy. This is the exact gomobile mobile/go.mod requires, so the binder
# and the bind's `go` support package stay on one version.
install_gomobile() {
  local gomobile_version
  gomobile_version="$(singbox_gomobile_version)"
  echo ">> installing sagernet/gomobile $gomobile_version (from sing-box v$singbox_version Makefile)"
  go install "github.com/sagernet/gomobile/cmd/gomobile@$gomobile_version"
  go install "github.com/sagernet/gomobile/cmd/gobind@$gomobile_version"
  gomobile init
}

# build_tenebra binds the wrapper AND libbox into ONE Tenebra.xcframework in a single
# gomobile pass over two packages: our wrapper (".", package tenebracore) and
# sing-box's libbox. gomobile resolves libbox from mobile/go.mod (pinned to
# v$singbox_version), so no sing-box clone is needed here — the tags/ldflags below
# ARE sing-box's own apple build settings, transcribed. The framework's module name
# is Tenebra (from -o), so Swift does `import Tenebra` and sees both
# TenebracoreGenerateConfig(...) and the Libbox* symbols. VERIFY that module/symbol
# spelling against the generated headers on the Mac (see ui-ios/App/TunnelManager.swift
# and ui-ios/Extension/*).
build_tenebra() {
  echo ">> building Tenebra.xcframework (wrapper + libbox) — compiles sing-box, slow"
  ( cd "$root/mobile"
    gomobile bind \
      -target "ios,iossimulator" \
      -iosversion "$ios_min" \
      -tags-not-macos=with_low_memory \
      -trimpath \
      -buildvcs=false \
      -ldflags "$libbox_ldflags" \
      -tags "$libbox_tags" \
      -o "$frameworks_dir/Tenebra.xcframework" \
      . github.com/sagernet/sing-box/experimental/libbox
  )
}

main() {
  require_macos
  install_gomobile
  build_tenebra
  echo ">> done. Tenebra.xcframework is in $frameworks_dir"
  echo "   next: cd ui-ios && xcodegen generate && open Tenebra.xcodeproj"
}

main "$@"
