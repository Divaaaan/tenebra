#!/usr/bin/env bash
# Fetches the sing-box binary and the RU rule-sets into
# ui-desktop/src-tauri/resources so the desktop app can bundle them. This is the
# macOS analog of fetch-resources.ps1; the pinned versions are kept in sync with
# it. These binaries are not checked in; run this before building the macOS
# bundle. Pinned versions keep builds reproducible.
#
# There is no wintun on macOS. The tun device is utun, which sing-box opens
# itself once it runs with privilege (see docs/porting/macos.md); nothing needs
# to be placed beside the binary for it.
set -euo pipefail

# Keep in sync with scripts/fetch-resources.ps1 ($singboxVersion).
singbox_version="1.13.13"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$script_dir")"
dest="$root/ui-desktop/src-tauri/resources"
mkdir -p "$dest"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# fetch downloads url to the given path, retrying transient failures the way the
# PowerShell script does.
fetch() {
  curl -L --retry 12 --retry-all-errors --retry-delay 2 -o "$2" "$1"
}

# Download and unpack a single-arch darwin sing-box, echoing the path to the
# extracted binary. sing-box ships one tarball per arch; the desktop bundle wants
# one universal binary, so both are fetched and lipo'd together below.
fetch_arch() {
  local arch="$1"
  local tarball="$work/sing-box-$arch.tar.gz"
  local url="https://github.com/SagerNet/sing-box/releases/download/v$singbox_version/sing-box-$singbox_version-darwin-$arch.tar.gz"
  fetch "$url" "$tarball"
  local out="$work/$arch"
  mkdir -p "$out"
  tar -xzf "$tarball" -C "$out"
  # The tarball extracts to sing-box-<version>-darwin-<arch>/sing-box.
  find "$out" -type f -name sing-box | head -n 1
}

arm64_bin="$(fetch_arch arm64)"
amd64_bin="$(fetch_arch amd64)"

if command -v lipo >/dev/null 2>&1; then
  # Tauri's universal-apple-darwin target expects every bundled binary to be
  # universal too, so stitch the two slices into one fat binary.
  lipo -create "$arm64_bin" "$amd64_bin" -output "$dest/sing-box"
  chmod +x "$dest/sing-box"
  echo "Built universal (arm64+amd64) sing-box $singbox_version"
else
  # No lipo (not on a macOS host, or the command-line tools are absent): keep the
  # per-arch binaries and fall back to the arm64 slice as the default, since the
  # hosted macOS runners are all Apple Silicon. A universal bundle still needs a
  # lipo pass on a real macOS host.
  # TODO(macos): run the lipo step on a macOS host/runner to produce a universal
  # sing-box before shipping a universal bundle.
  cp "$arm64_bin" "$dest/sing-box-arm64"
  cp "$amd64_bin" "$dest/sing-box-amd64"
  cp "$arm64_bin" "$dest/sing-box"
  chmod +x "$dest/sing-box" "$dest/sing-box-arm64" "$dest/sing-box-amd64"
  echo "lipo unavailable; wrote per-arch sing-box $singbox_version and defaulted to arm64 (TODO: lipo into a universal binary on macOS)"
fi

# RU geo rule-sets, shipped locally so smart routing loads them from disk instead
# of downloading them from GitHub at startup (the download blocks sing-box for
# ~10s when raw.githubusercontent.com is throttled). These are the official
# SagerNet rule-set releases, pinned to the branch the routing layer references.
# Identical to the URLs fetch-resources.ps1 pulls.
fetch "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs" "$dest/geoip-ru.srs"
fetch "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs" "$dest/geosite-ru.srs"

# Ad/tracker blocklist for the opt-in DNS ad-blocker. Same SagerNet source and
# license as the RU geosite set (compiled from the v2fly community domain list).
# It is bundled and loaded strictly as a LOCAL rule-set — never fetched at
# runtime — so it can never reintroduce the startup freeze a remote rule-set caused.
fetch "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs" "$dest/geosite-ads.srs"

echo "Fetched sing-box $singbox_version and RU + ads rule-sets into $dest"
