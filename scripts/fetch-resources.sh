#!/usr/bin/env bash
# Fetches the sing-box binary, the ByeDPI engine and the RU rule-sets into
# ui-desktop/src-tauri/resources so the desktop app can bundle them. This is the
# Unix analog of fetch-resources.ps1 and serves both macOS and Linux: the pinned
# version, the rule-sets, the retry policy and the checksum discipline are
# identical on the two, and only the sing-box artifact differs, so one script
# keeps a single copy of the .srs pins instead of letting two files drift apart.
# The pinned versions are kept in sync with the PowerShell script. These binaries
# are not checked in; run this before building the desktop bundle. Pinned
# versions keep builds reproducible.
#
# Neither Unix target needs a driver payload beside the binary the way Windows
# needs wintun.dll: the tun device is utun on macOS and /dev/net/tun on Linux,
# and sing-box opens either itself once it runs with privilege (see
# docs/porting/macos.md and docs/porting/linux.md).
#
# The two Unix targets do differ on ByeDPI: upstream publishes Windows and Linux
# builds and no darwin one, so only the Linux branch fetches it — see the Darwin
# case at the bottom of this script.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: fetch-resources.sh [--arch <amd64|arm64>]

Downloads the pinned sing-box build for this host plus the RU and ads rule-sets
into ui-desktop/src-tauri/resources. On Linux it also downloads the pinned ByeDPI
engine (ciadpi); upstream has no macOS build, so that step is skipped there.

  --arch  Linux only: fetch this architecture instead of the host's, for a cross
          build. macOS always produces a universal (arm64 + amd64) binary, so it
          rejects the flag.
EOF
}

# Requested Linux architecture, empty for "whatever this host is".
target_arch=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --arch)
      target_arch="${2:-}"
      [ -n "$target_arch" ] || { echo "--arch needs an architecture (amd64 or arm64)" >&2; exit 2; }
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1 (see --help)" >&2
      exit 2
      ;;
  esac
done

# Keep in sync with scripts/fetch-resources.ps1 ($singboxVersion).
singbox_version="1.13.13"

# ByeDPI, the DPI-bypass engine; keep in sync with fetch-resources.ps1
# ($byedpiVersion) and with packaging/arch/PKGBUILD. Upstream tags the release
# `v0.17.3` but names its archives `byedpi-17.3-*`, so the archive name is derived
# from the tag instead of being a second literal that can fall out of step.
byedpi_version="0.17.3"
byedpi_asset_version="${byedpi_version#0.}"

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
# these variables feed (the GitHub darwin and linux sing-box tarballs, the ByeDPI
# linux tarballs and the raw .srs files), run
#   shasum -a 256 <file>
# on each, and paste the digests here. The .srs sets are rebuilt periodically on
# the SagerNet `rule-set` branch, so their digests move even without a version
# change — re-pin them whenever the bundled copies are refreshed. Mirror every
# change into scripts/fetch-resources.ps1 (identical .srs digests; Windows
# sing-box and ByeDPI digests for that script) and into packaging/arch/PKGBUILD,
# which pins the same linux-amd64 artifacts and the same three rule-sets in its
# source()/sha256sums() arrays so makepkg can verify them itself.
singbox_sha256_darwin_arm64="4ac414d4ede9ec21bc79d8ccf40b4679429203b9e06ad96d2d8d34c0fe940558"
singbox_sha256_darwin_amd64="477afd64ad7751214f01338ba244265ecc223966ddb58214963f526dca7f424e"
singbox_sha256_linux_amd64="bb99cabf47694625db421ee17898f36cdc1f9c2cb5decf65b12bac8d8437e842"
singbox_sha256_linux_arm64="d7fab87b921933eb281d8ee7bd5377cdd8228089f1f7c807c9363a6a2329286c"
byedpi_sha256_linux_amd64="98f73c32eacb571ebd88d790f6376ed9e70f02d44c1fe862472ecea75cd7117d"
byedpi_sha256_linux_arm64="d5806504e159e8119ede5af1164a609251b0520cd4483a7db611b6d238b9404b"
geoip_ru_sha256="8bc18433e5d5b0644ba2a9ff74cd03428ba4f4e388b3c409f182de930e3c3170"
geosite_ru_sha256="3fb41849eefac86a4e65a86da3b868ecd40512e4d3f097ee325474f4cd401f76"
geosite_ads_sha256="ca44c97fce76f4f889e08bbc28e80d497a43239328e09f760129844057a2780a"

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

# Download and unpack a single-arch sing-box release tarball, echoing the path to
# the extracted binary. sing-box ships one tarball per (os, arch); the tarball is
# checksum-verified before it is unpacked, so nothing unverified is ever written
# where the bundle step will pick it up.
#
# The archive carries a LICENSE and a libcronet.so/.dylib beside the executable.
# libcronet is dlopen'd only by sing-box's Naive outbound, a protocol Tenebra's
# generator never emits, so it is deliberately left out of the bundle rather than
# shipped as a ~40 MB unused blob.
fetch_arch() {
  local os="$1" arch="$2" expected="$3"
  local tarball="$work/sing-box-$os-$arch.tar.gz"
  local url="https://github.com/SagerNet/sing-box/releases/download/v$singbox_version/sing-box-$singbox_version-$os-$arch.tar.gz"
  fetch "$url" "$tarball"
  verify_sha256 "$tarball" "$expected"
  local out="$work/$os-$arch"
  mkdir -p "$out"
  tar -xzf "$tarball" -C "$out"
  # The tarball extracts to sing-box-<version>-<os>-<arch>/sing-box.
  find "$out" -type f -name sing-box | head -n 1
}

# fetch_singbox_darwin stitches the two darwin slices into the one universal
# binary Tauri's universal-apple-darwin target expects of every bundled binary.
fetch_singbox_darwin() {
  local arm64_bin amd64_bin
  arm64_bin="$(fetch_arch darwin arm64 "$singbox_sha256_darwin_arm64")"
  amd64_bin="$(fetch_arch darwin amd64 "$singbox_sha256_darwin_amd64")"

  if command -v lipo >/dev/null 2>&1; then
    lipo -create "$arm64_bin" "$amd64_bin" -output "$dest/sing-box"
    chmod +x "$dest/sing-box"
    echo "Built universal (arm64+amd64) sing-box $singbox_version"
  else
    # No lipo (not on a macOS host, or the command-line tools are absent): keep
    # the per-arch binaries and fall back to the arm64 slice as the default,
    # since the hosted macOS runners are all Apple Silicon. A universal bundle
    # still needs a lipo pass on a real macOS host.
    # TODO(macos): run the lipo step on a macOS host/runner to produce a
    # universal sing-box before shipping a universal bundle.
    cp "$arm64_bin" "$dest/sing-box-arm64"
    cp "$amd64_bin" "$dest/sing-box-amd64"
    cp "$arm64_bin" "$dest/sing-box"
    chmod +x "$dest/sing-box" "$dest/sing-box-arm64" "$dest/sing-box-amd64"
    echo "lipo unavailable; wrote per-arch sing-box $singbox_version and defaulted to arm64 (TODO: lipo into a universal binary on macOS)"
  fi
}

# linux_arch maps `uname -m` onto sing-box's release naming, or echoes an
# explicitly requested architecture back after validating it. Only the two 64-bit
# desktop architectures are pinned: sing-box also publishes 386 and armv7 builds,
# but the bundle they would go into is a webkit2gtk Tauri app, and 32-bit Linux
# desktops are not a target — pinning a digest nobody exercises would be dead
# weight that still has to be re-cut on every version bump.
linux_arch() {
  local requested="$1" machine
  if [ -n "$requested" ]; then
    case "$requested" in
      amd64|arm64) echo "$requested"; return 0 ;;
      *) echo "unsupported --arch $requested; this script pins amd64 and arm64" >&2; return 1 ;;
    esac
  fi
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *)
      echo "unsupported Linux architecture $machine; this script pins amd64 and arm64" >&2
      echo "pass --arch to cross-fetch one of them, or add a pin for $machine" >&2
      return 1
      ;;
  esac
}

# fetch_singbox_linux installs the single-architecture ELF for the target. There
# is no lipo equivalent on Linux — an ELF holds exactly one architecture — so the
# bundle is per-arch by construction and the host's architecture is the default.
# The bundled name stays plain `sing-box`, matching the darwin layout the core's
# TENEBRA_SINGBOX resolution and the installers expect.
fetch_singbox_linux() {
  local arch bin expected
  arch="$(linux_arch "$target_arch")"
  case "$arch" in
    amd64) expected="$singbox_sha256_linux_amd64" ;;
    arm64) expected="$singbox_sha256_linux_arm64" ;;
  esac
  bin="$(fetch_arch linux "$arch" "$expected")"
  cp "$bin" "$dest/sing-box"
  chmod +x "$dest/sing-box"
  echo "Fetched linux-$arch sing-box $singbox_version"
}

# fetch_byedpi_linux installs ByeDPI (ciadpi), the userspace DPI-bypass engine the
# core spawns beside sing-box when the bypass is on. It needs no driver and no
# privilege — it is a plain loopback SOCKS5 proxy — but it is bundled next to
# sing-box because the core resolves the two the same way. Upstream ships one
# statically linked executable per architecture, named after that architecture
# inside the tarball; the bundled name is the plain `ciadpi` the core looks for.
fetch_byedpi_linux() {
  local arch asset expected tarball out
  arch="$(linux_arch "$target_arch")"
  case "$arch" in
    amd64) asset="x86_64"; expected="$byedpi_sha256_linux_amd64" ;;
    arm64) asset="aarch64"; expected="$byedpi_sha256_linux_arm64" ;;
  esac
  tarball="$work/byedpi-$asset.tar.gz"
  fetch "https://github.com/hufrea/byedpi/releases/download/v$byedpi_version/byedpi-$byedpi_asset_version-$asset.tar.gz" "$tarball"
  verify_sha256 "$tarball" "$expected"
  out="$work/byedpi-$asset"
  mkdir -p "$out"
  tar -xzf "$tarball" -C "$out"
  cp "$out/ciadpi-$asset" "$dest/ciadpi"
  chmod +x "$dest/ciadpi"
  echo "Fetched linux-$arch ByeDPI $byedpi_version"
}

host_os="$(uname -s)"
case "$host_os" in
  Darwin)
    [ -z "$target_arch" ] || { echo "--arch is Linux-only; the macOS bundle is always universal" >&2; exit 2; }
    fetch_singbox_darwin
    # No ByeDPI on macOS. Upstream publishes Windows, Linux, and nothing for
    # darwin, and compiling it here would give up the property every other
    # bundled artifact has — a pinned digest over an upstream binary — while
    # making the bundle depend on the build host's toolchain. So the macOS bundle
    # ships without the engine and the DPI bypass reports itself unsupported
    # there rather than turning on and doing nothing; see docs/architecture.md.
    echo "Skipped ByeDPI: upstream publishes no macOS build, so the DPI bypass is not bundled on darwin"
    ;;
  Linux)
    fetch_singbox_linux
    fetch_byedpi_linux
    ;;
  *)
    echo "unsupported host $host_os; use scripts/fetch-resources.ps1 on Windows" >&2
    exit 1
    ;;
esac

# RU geo rule-sets, shipped locally so smart routing loads them from disk instead
# of downloading them from GitHub at startup (the download blocks sing-box for
# ~10s when raw.githubusercontent.com is throttled). These are the official
# SagerNet rule-sets, pinned to immutable commits rather than the rolling
# `rule-set` branch (which regenerates daily and drifts out from under the SHA
# pins). Identical to the URLs fetch-resources.ps1 pulls; bump commit + SHA together.
fetch "https://raw.githubusercontent.com/SagerNet/sing-geoip/a508a0a09d30111e0ab5a0d9a3de1aff832d72b4/geoip-ru.srs" "$dest/geoip-ru.srs"
verify_sha256 "$dest/geoip-ru.srs" "$geoip_ru_sha256"
fetch "https://raw.githubusercontent.com/SagerNet/sing-geosite/02b7bc85184c7fa94ccdfe9a35b7f4a169b28b4d/geosite-category-ru.srs" "$dest/geosite-ru.srs"
verify_sha256 "$dest/geosite-ru.srs" "$geosite_ru_sha256"

# Ad/tracker blocklist for the opt-in DNS ad-blocker. Same SagerNet source and
# license as the RU geosite set (compiled from the v2fly community domain list).
# It is bundled and loaded strictly as a LOCAL rule-set — never fetched at
# runtime — so it can never reintroduce the startup freeze a remote rule-set caused.
fetch "https://raw.githubusercontent.com/SagerNet/sing-geosite/02b7bc85184c7fa94ccdfe9a35b7f4a169b28b4d/geosite-category-ads-all.srs" "$dest/geosite-ads.srs"
verify_sha256 "$dest/geosite-ads.srs" "$geosite_ads_sha256"

echo "Fetched sing-box $singbox_version and RU + ads rule-sets into $dest"
