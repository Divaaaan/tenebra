#!/usr/bin/env bash
# Builds the single gomobile artifact the Android client links — the mirror of the
# iOS build in scripts/build-libbox.sh (see docs/porting/android.md and
# ui-android/README.md):
#
#   tenebra.aar — ONE .aar carrying BOTH Tenebra's config generator (the ../mobile
#                 wrapper over core-bridge) AND sing-box's experimental/libbox
#                 engine, bound together in a single gomobile pass.
#
# Why one artifact and not two: a standalone core .aar and a standalone libbox.aar
# each bundle their own Go runtime and their own copy of the gomobile support
# package `go` (go.Seq, go.Universe). Linking both makes Android's D8 fail on
# duplicate go/Seq + go/Universe classes and, even past that, loads two Go runtimes
# in one process. Binding the wrapper and libbox in one pass yields one runtime and
# one `go` package, with io.nekohasekai.tenebracore.Tenebracore and the
# io.nekohasekai.libbox.* classes side by side. See ../mobile/tools.go.
#
# It lands in ui-android/app/libs/ for Gradle to pick up.
#
# ============================================================================
#  Linux/WSL/macOS + Android NDK r28 + OpenJDK 17 + Go >= 1.24.7 REQUIRED.
# ============================================================================
# This was authored on Windows and has NOT been executed here; the first real run
# is on CI (.github/workflows/android.yml). Treat it as an executable plan until
# that run is green. Unlike the Apple build, the .aar CAN be produced on Linux —
# gomobile's android bind shells out to the NDK's clang, not to Xcode — which is
# exactly why Android gets a CI job and iOS does not.
#
# Prerequisites:
#   - Go >= 1.24.7 (what the pinned sing-box tag requires when libbox is linked).
#   - Android NDK r28, discoverable via ANDROID_NDK_HOME (or ANDROID_NDK_ROOT, or
#     $ANDROID_HOME/ndk/...). gomobile needs it: the bind emits a JNI/C bridge and
#     compiles libbox's cgo (gVisor, Cronet) with the NDK's clang.
#   - OpenJDK 17 for the Gradle build that consumes this .aar (not this script).
#   - make, git. This repo checked out; run from anywhere (paths resolve here).
set -euo pipefail

# --- Pinned versions -------------------------------------------------------
# Keep sing-box in sync with scripts/fetch-resources.sh / .ps1 ($singbox_version),
# scripts/build-libbox.sh AND mobile/go.mod: one engine version so one config
# generator targets one schema. The bind below compiles libbox from the module
# graph (mobile/go.mod), so that pin is the one that actually decides the engine
# version; this clone is only used to install the matching gomobile fork.
singbox_version="1.13.13"

# This script deliberately does NOT pin a gomobile version of its own. sing-box's
# `make lib_install` installs the exact sagernet/gomobile fork the pinned tag
# expects (v0.1.12 for 1.13.13), which is also what mobile/go.mod requires, so the
# binder and the bind's support package can never drift apart. The version lives in
# sing-box's Makefile — the single source of truth — never here.

# minSdk / -androidapi for the bind. 23 matches libbox's own main-variant android
# API level (sing-box's build_libbox builds the API-23 "main" libbox.aar) and the
# app's minSdk 23, so the fused artifact shares that floor.
android_api="23"

# libbox build tags — replicated EXACTLY from sing-box's own build entrypoint so the
# fused engine is bit-for-bit what upstream ships. Source: cmd/internal/build_libbox/
# main.go at tag v$singbox_version, the sharedTags slice (two append() calls) that
# the API-23 "main" android variant is built with (buildAndroid(): mainTags =
# sharedTags, no debug). A MISSING tag silently drops a protocol from the runtime,
# so this list is not to be trimmed by hand. If singbox_version changes, re-read
# that file and update this verbatim.
libbox_tags="with_gvisor,with_quic,with_wireguard,with_utls,with_naive_outbound,with_clash_api,badlinkname,tfogo_checklinkname0,with_tailscale,ts_omit_logtail,ts_omit_ssh,ts_omit_drive,ts_omit_taildrop,ts_omit_webclient,ts_omit_doctor,ts_omit_capture,ts_omit_kube,ts_omit_aws,ts_omit_synology,ts_omit_bird"

# Linker flags, also from that file's sharedFlags. -checklinkname=0 is load-bearing:
# it pairs with the badlinkname / tfogo_checklinkname0 tags and lets sing-box and
# tfo-go keep their //go:linkname hacks, which Go >= 1.23 would otherwise reject at
# link time. The -X's set the version libbox reports and disable multipath TCP by
# default, matching upstream.
libbox_ldflags="-X github.com/sagernet/sing-box/constant.Version=v$singbox_version -X internal/godebug.defaultGODEBUG=multipathtcp=0 -s -w -buildid= -checklinkname=0"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$script_dir")"
libs_dir="$root/ui-android/app/libs"

# --- Toolchain guard -------------------------------------------------------
guard_host() {
  case "$(uname -s)" in
    Linux | Darwin) : ;; # native Linux/macOS and WSL (WSL reports Linux)
    MINGW* | MSYS* | CYGWIN*)
      echo "warning: building the Android .aar on native Windows is unverified;" >&2
      echo "         run this under WSL or on Linux/macOS. Continuing at your own risk." >&2
      ;;
    *)
      echo "warning: unrecognised host $(uname -s); continuing at your own risk." >&2
      ;;
  esac
  command -v go >/dev/null || { echo "error: Go >= 1.24.7 not found" >&2; exit 1; }
  command -v git >/dev/null || { echo "error: git not found" >&2; exit 1; }
  command -v make >/dev/null || {
    echo "error: make not found — sing-box's lib_install target needs it" >&2
    exit 1
  }
  if [ -z "${ANDROID_NDK_HOME:-}${ANDROID_NDK_ROOT:-}${ANDROID_HOME:-}" ]; then
    echo "warning: none of ANDROID_NDK_HOME / ANDROID_NDK_ROOT / ANDROID_HOME is set;" >&2
    echo "         gomobile needs the Android NDK (r28) to bind. Install it and export" >&2
    echo "         ANDROID_NDK_HOME, or this build will fail at the first bind." >&2
  fi
}

# gopath_bin echoes the directory `go install` drops binaries into, so gomobile
# and gobind can be put on PATH without assuming the host already has it there.
gopath_bin() {
  local gobin
  gobin="$(go env GOBIN)"
  [ -n "$gobin" ] || gobin="$(go env GOPATH)/bin"
  printf '%s\n' "$gobin"
}

main() {
  guard_host
  mkdir -p "$libs_dir"

  local work
  work="$(mktemp -d)"
  trap 'rm -rf "${work:-}"' EXIT

  # Clone sing-box ONLY to install the gomobile fork it pins — its `make lib_install`
  # installs the exact sagernet/gomobile the tag expects, sourcing the version from
  # its Makefile so this script never hardcodes it. The engine SOURCE that gets bound
  # does NOT come from this clone: the bind runs in mobile/ and resolves
  # experimental/libbox from mobile/go.mod (pinned to the same v$singbox_version).
  echo ">> cloning sing-box v$singbox_version (for its gomobile fork only)"
  git clone --depth 1 --branch "v$singbox_version" \
    https://github.com/SagerNet/sing-box "$work/sing-box"

  echo ">> installing the sagernet/gomobile fork via sing-box's lib_install"
  (cd "$work/sing-box" && make lib_install)
  PATH="$(gopath_bin):$PATH"
  export PATH

  # --- The single combined bind --------------------------------------------
  # One gomobile pass over TWO packages: our wrapper (".", package tenebracore) and
  # sing-box's libbox. gomobile fuses them into one .aar with one Go runtime and one
  # `go` support package. -javapkg io.nekohasekai keeps libbox at io.nekohasekai.
  # libbox.* (so ui-android/bg's imports are unchanged) and puts our class at
  # io.nekohasekai.tenebracore.Tenebracore. The tags/ldflags are sing-box's own (see
  # above); our wrapper is pure Go and needs none of them, so listing them is safe.
  #
  # No -libname: upstream passes -libname=box, which on Android only affects an
  # internal name and is redundant once -o names the artifact. The output name is
  # ours (tenebra.aar); the bind is self-consistent either way.
  echo ">> binding tenebra.aar (wrapper + libbox) — the slow one, compiles sing-box"
  (
    cd "$root/mobile"
    gomobile bind \
      -target android \
      -androidapi "$android_api" \
      -javapkg io.nekohasekai \
      -trimpath \
      -buildvcs=false \
      -ldflags "$libbox_ldflags" \
      -tags "$libbox_tags" \
      -o "$libs_dir/tenebra.aar" \
      . github.com/sagernet/sing-box/experimental/libbox
  )

  echo ">> done. Android artifact is in $libs_dir:"
  echo "     tenebra.aar  (our config generator + the sing-box engine, one bind)"
  echo "   next: ./gradlew :app:assembleDebug -p ui-android"
}

main "$@"
