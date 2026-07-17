#!/usr/bin/env bash
# Builds the two gomobile xcframeworks the iOS client links (see
# docs/porting/ios.md and ui-ios/README.md):
#
#   1. TenebraCore.xcframework  — Tenebra's own config generator (core-bridge),
#                                 exposing GenerateConfig(requestJSON) -> configJSON.
#   2. Libbox.xcframework       — sing-box's experimental/libbox engine, built from
#                                 the pinned sing-box tag, used UNMODIFIED.
#
# Both are dropped into ui-ios/Frameworks/ for XcodeGen (project.yml) to reference.
#
# ============================================================================
#  macOS + Xcode + Go >= 1.24.7 REQUIRED. This is NOT runnable on this host.
# ============================================================================
# gomobile's Apple bind shells out to Xcode/clang for the Objective-C bridge (cgo
# is inherently on), so the Apple frameworks cannot be produced on Windows or
# Linux — only on a Mac with the full Xcode toolchain. This script was authored on
# Windows and has NOT been executed; the commands are transcribed from the pinned
# sing-box tag's own Makefile and the porting research, and must be validated on a
# real Mac. Treat it as an executable plan, not a proven build.
#
# Prerequisites on the Mac:
#   - Xcode + command line tools (xcode-select --install), an iOS SDK present.
#   - Go >= 1.24.7 (the pinned sing-box tag requires it when libbox is linked).
#   - This repo checked out; run from anywhere (paths are resolved from the script).
set -euo pipefail

# --- Pinned versions -------------------------------------------------------
# Keep sing-box in sync with scripts/fetch-resources.sh / .ps1 ($singbox_version).
# The desktop sidecar and the iOS libbox must be the same engine version so one
# config generator targets one schema.
singbox_version="1.13.13"
# The gomobile version is NOT hardcoded here. sing-box's Makefile at the pinned
# tag installs a specific SagerNet gomobile fork (NOT upstream golang.org/x/mobile,
# which repeatedly breaks on new Xcode releases), and that same fork has to build
# both frameworks this script produces — so the version is read straight from the
# tag's Makefile (see singbox_gomobile_version) rather than pinned a second time
# here, where it could drift from what `make lib_install` installs for libbox below.

# iOS deployment floor — must match project.yml (NE gets the 50 MB cap on iOS 15+).
ios_min="15.0"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$script_dir")"
frameworks_dir="$root/ui-ios/Frameworks"
mkdir -p "$frameworks_dir"

# --- Toolchain guard -------------------------------------------------------
require_macos() {
  if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "error: this script builds Apple xcframeworks and only runs on macOS." >&2
    echo "       (host is $(uname -s); see the header of this file.)" >&2
    exit 1
  fi
  command -v go >/dev/null    || { echo "error: Go >= 1.24.7 not found" >&2; exit 1; }
  command -v xcodebuild >/dev/null || { echo "error: Xcode not found" >&2; exit 1; }
  command -v curl >/dev/null  || { echo "error: curl not found (needed to resolve the gomobile version from sing-box's Makefile)" >&2; exit 1; }
}

# singbox_gomobile_version reads the SagerNet gomobile fork version the pinned
# sing-box tag installs in its `lib_install` target, straight from that tag's
# Makefile, so this script never carries a second gomobile pin that could drift
# from the one the libbox build (make lib_install, below) actually uses. The tag
# is immutable, so this resolves to a fixed version per pinned singbox_version.
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
# hardcoded copy. Binding TenebraCore with this exact gomobile keeps it on the same
# binder the libbox build (make lib_install) uses a few steps later.
install_gomobile() {
  local gomobile_version
  gomobile_version="$(singbox_gomobile_version)"
  echo ">> installing sagernet/gomobile $gomobile_version (from sing-box v$singbox_version Makefile)"
  go install "github.com/sagernet/gomobile/cmd/gomobile@$gomobile_version"
  go install "github.com/sagernet/gomobile/cmd/gobind@$gomobile_version"
  gomobile init
}

# build_tenebra_core binds core-bridge into TenebraCore.xcframework. The bridge
# carries the `ios || android` build tag (it is the shared mobile binding — the
# same package binds to an Android .aar); gomobile targets GOOS=ios here, which
# activates the `ios` side of it automatically. Our core imports NO sing-box, so it
# needs none of libbox's build tags — it is a pure stdlib generator.
build_tenebra_core() {
  echo ">> building TenebraCore.xcframework"
  ( cd "$root"
    gomobile bind \
      -target "ios,iossimulator" \
      -iosversion "$ios_min" \
      -trimpath \
      -o "$frameworks_dir/TenebraCore.xcframework" \
      ./core-bridge
  )
}

# build_libbox builds sing-box's Libbox.xcframework from the pinned tag using
# sing-box's OWN build entrypoint, so the engine matches upstream exactly. The
# canonical path is the repo's `make lib_apple` (which runs
# `go run ./cmd/internal/build_libbox -target apple`); we clone the tag and invoke
# it rather than reproducing the long tag/ldflags list by hand.
#
# NOTE on with_naive_outbound: sing-box's own libbox build INCLUDES it, and the
# official SFI app ships that way, so it is kept here. The porting research flags
# that with_naive_outbound pulls in Cronet, whose C++ runtime collides at link
# time with other C++ libraries (e.g. libsignal). Tenebra links none of those, so
# the default build is fine; if that ever changes, rebuild libbox WITHOUT the tag
# via a hand-rolled `gomobile bind` (see docs/porting/ios.md#building-the-core-gomobile).
build_libbox() {
  echo ">> building Libbox.xcframework from sing-box v$singbox_version"
  local work
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' RETURN

  git clone --depth 1 --branch "v$singbox_version" \
    https://github.com/SagerNet/sing-box "$work/sing-box"

  ( cd "$work/sing-box"
    # Installs the pinned gomobile fork the tag expects, then produces
    # Libbox.xcframework next to the Makefile.
    make lib_install
    make lib_apple
  )

  rm -rf "$frameworks_dir/Libbox.xcframework"
  cp -R "$work/sing-box/Libbox.xcframework" "$frameworks_dir/Libbox.xcframework"
}

main() {
  require_macos
  install_gomobile
  build_tenebra_core
  build_libbox
  echo ">> done. xcframeworks are in $frameworks_dir"
  echo "   next: cd ui-ios && xcodegen generate && open Tenebra.xcodeproj"
}

main "$@"
