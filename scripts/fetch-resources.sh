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

# SHA-256 pins for every artifact fetched below. These downloads are bundled
# verbatim into the signed release, so a swapped upstream artifact — a poisoned
# GitHub release, a cache-poisoned raw.githubusercontent.com, or a
# TLS-intercepting build box — would otherwise be signed and shipped as a
# privileged tunnel binary. Each fetch is checked against its pin and the script
# aborts on any mismatch, so a tampered artifact never reaches the bundle.
#
# The .srs pins are the same files the PowerShell script fetches, so their
# digests are byte-for-byte identical to the ones in scripts/fetch-resources.ps1;
# the sing-box pins there are the Windows build and differ by platform.
#
# To refresh on a version/rule-set bump: bump the version, download the same URLs
# these variables feed (the GitHub darwin tarballs and the raw .srs files), run
#   shasum -a 256 <file>
# on each, and paste the digests here. The .srs sets are rebuilt periodically on
# the SagerNet `rule-set` branch, so their digests move even without a version
# change — re-pin them whenever the bundled copies are refreshed. Mirror every
# change into scripts/fetch-resources.ps1 (identical .srs digests; Windows
# sing-box digest for that script).
singbox_sha256_darwin_arm64="4ac414d4ede9ec21bc79d8ccf40b4679429203b9e06ad96d2d8d34c0fe940558"
singbox_sha256_darwin_amd64="477afd64ad7751214f01338ba244265ecc223966ddb58214963f526dca7f424e"
geoip_ru_sha256="8bc18433e5d5b0644ba2a9ff74cd03428ba4f4e388b3c409f182de930e3c3170"
geosite_ru_sha256="3fb41849eefac86a4e65a86da3b868ecd40512e4d3f097ee325474f4cd401f76"
geosite_ads_sha256="0e8e5a818e516c7fdffb585d3066129bbbc7759fe80f414055694ce8392e83c8"

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

# sha256_of echoes the lowercase hex SHA-256 of a file, using whichever of the
# usual CLIs the host ships (shasum on macOS, sha256sum on most Linux runners).
sha256_of() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

# verify_sha256 aborts the build unless $1 hashes to the pinned digest $2. A
# mismatch means the fetched artifact is not the one these pins were cut against,
# so it must never be signed into a release — treat it as fatal, not a warning.
verify_sha256() {
  local file="$1" expected="$2" actual
  actual="$(sha256_of "$file")"
  if [ "$actual" != "$expected" ]; then
    echo "checksum mismatch for $file" >&2
    echo "  expected $expected" >&2
    echo "  actual   $actual" >&2
    echo "refusing to bundle an unverified artifact; see the SHA-256 pins in $0" >&2
    exit 1
  fi
}

# Download and unpack a single-arch darwin sing-box, echoing the path to the
# extracted binary. sing-box ships one tarball per arch; the desktop bundle wants
# one universal binary, so both are fetched and lipo'd together below. The
# tarball is checksum-verified before it is unpacked.
fetch_arch() {
  local arch="$1"
  local expected="$2"
  local tarball="$work/sing-box-$arch.tar.gz"
  local url="https://github.com/SagerNet/sing-box/releases/download/v$singbox_version/sing-box-$singbox_version-darwin-$arch.tar.gz"
  fetch "$url" "$tarball"
  verify_sha256 "$tarball" "$expected"
  local out="$work/$arch"
  mkdir -p "$out"
  tar -xzf "$tarball" -C "$out"
  # The tarball extracts to sing-box-<version>-darwin-<arch>/sing-box.
  find "$out" -type f -name sing-box | head -n 1
}

arm64_bin="$(fetch_arch arm64 "$singbox_sha256_darwin_arm64")"
amd64_bin="$(fetch_arch amd64 "$singbox_sha256_darwin_amd64")"

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
verify_sha256 "$dest/geoip-ru.srs" "$geoip_ru_sha256"
fetch "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs" "$dest/geosite-ru.srs"
verify_sha256 "$dest/geosite-ru.srs" "$geosite_ru_sha256"

# Ad/tracker blocklist for the opt-in DNS ad-blocker. Same SagerNet source and
# license as the RU geosite set (compiled from the v2fly community domain list).
# It is bundled and loaded strictly as a LOCAL rule-set — never fetched at
# runtime — so it can never reintroduce the startup freeze a remote rule-set caused.
fetch "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs" "$dest/geosite-ads.srs"
verify_sha256 "$dest/geosite-ads.srs" "$geosite_ads_sha256"

echo "Fetched sing-box $singbox_version and RU + ads rule-sets into $dest"
