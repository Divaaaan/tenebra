#!/usr/bin/env bash
# Builds the two gomobile artifacts the Android client links — the mirror of the
# iOS build in scripts/build-libbox.sh (see docs/porting/android.md; ui-android's
# own README, once it lands, is the build/status companion):
#
#   1. tenebra-core.aar — Tenebra's own config generator (the shared core-bridge
#                         package), exposing GenerateConfig(requestJSON) -> configJSON
#                         plus ImportSubscription / OrderNodes / Version.
#   2. libbox.aar       — sing-box's experimental/libbox engine, built from the
#                         pinned sing-box tag with the tag's OWN make targets and
#                         used UNMODIFIED (package io.nekohasekai.libbox).
#
# Both land in ui-android/app/libs/ for Gradle to pick up.
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
#     $ANDROID_HOME/ndk/...). gomobile needs it even for the pure-Go core .aar,
#     because the bind emits a JNI/C bridge the NDK's clang compiles.
#   - OpenJDK 17 for the Gradle build that consumes these .aars (not this script).
#   - make, git. This repo checked out; run from anywhere (paths resolve here).
set -euo pipefail

# --- Pinned versions -------------------------------------------------------
# Keep sing-box in sync with scripts/fetch-resources.sh / .ps1 ($singbox_version)
# and with scripts/build-libbox.sh: one engine version so one config generator
# targets one schema.
singbox_version="1.13.13"

# This script deliberately does NOT pin a gomobile version. sing-box's own
# `make lib_install` installs the exact sagernet/gomobile fork the pinned tag
# expects (v0.1.12 for 1.13.13), and BOTH artifacts are bound with that same
# gomobile, so the engine and our core can never drift onto two different binders.
# The version lives in sing-box's Makefile — the single source of truth — never
# here. (scripts/build-libbox.sh derives the same version from that Makefile too.)

# minSdk for the bind. 23 matches libbox's own main-variant android API level
# (sing-box's build_libbox uses -androidapi 23), so both .aars share a floor.
android_api="23"

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
    echo "error: make not found — sing-box's lib_install / lib_android targets need it" >&2
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
  trap 'rm -rf "$work"' EXIT

  # sing-box is needed for BOTH artifacts: its `make lib_install` provides the
  # gomobile fork we also bind our core with, and its `make lib_android` builds
  # libbox. Clone it once, shallow, at the pinned tag.
  echo ">> cloning sing-box v$singbox_version"
  git clone --depth 1 --branch "v$singbox_version" \
    https://github.com/SagerNet/sing-box "$work/sing-box"
  local singbox="$work/sing-box"

  # Install the gomobile fork the tag pins — this is sing-box's own lib_install —
  # then put it on PATH for the binds below. Done up front (not inside the libbox
  # step) precisely because the core bind needs the same gomobile; sourcing the
  # version from the Makefile here is what lets this script avoid a hardcoded pin.
  # No `gomobile init`: modern gomobile binds on demand, and sing-box's proven
  # path (lib_install -> lib_android) runs no init either. If a future toolchain
  # needs it, add `gomobile init` right here.
  echo ">> installing the sagernet/gomobile fork via sing-box's lib_install"
  (cd "$singbox" && make lib_install)
  PATH="$(gopath_bin):$PATH"
  export PATH

  # --- Artifact 1: our config generator ------------------------------------
  # Pure stdlib + core/, no sing-box import, so it needs none of libbox's build
  # tags. gomobile sets GOOS=android, which activates the `android` side of the
  # `//go:build ios || android` bridge in core-bridge automatically. Rebuilt every
  # run (it is cheap) so a change under core-bridge/ or core/ is always reflected;
  # CI does NOT cache this one, only the slow engine .aar below.
  echo ">> binding tenebra-core.aar from ./core-bridge"
  (
    cd "$root"
    gomobile bind \
      -target android \
      -androidapi "$android_api" \
      -javapkg com.tenebra.core \
      -trimpath \
      -o "$libs_dir/tenebra-core.aar" \
      ./core-bridge
  )

  # --- Artifact 2: the sing-box engine -------------------------------------
  # Built from the tag's OWN `make lib_android` (= go run ./cmd/internal/build_libbox
  # -target android) so the engine is bit-for-bit upstream; every Tenebra protocol
  # is a subset of what that build ships. It is the slow step — it compiles the Go
  # runtime, gVisor and quic-go — and its only input is the sing-box version, so CI
  # caches this .aar under an engine-version key and we skip the rebuild when it is
  # already present (idempotent: a second local run reuses it too).
  if [ -f "$libs_dir/libbox.aar" ]; then
    echo ">> libbox.aar already present (cache hit / prior run); skipping the engine build"
  else
    echo ">> building libbox.aar from sing-box v$singbox_version (the slow one)"
    (cd "$singbox" && make lib_android)
    cp "$singbox/libbox.aar" "$libs_dir/libbox.aar"
  fi

  echo ">> done. Android .aars are in $libs_dir:"
  echo "     tenebra-core.aar  (our config generator)"
  echo "     libbox.aar        (sing-box engine, unmodified)"
  echo "   next: ./gradlew :app:assembleDebug -p ui-android"
}

main "$@"
