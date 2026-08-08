# Fetches the sing-box binary, wintun.dll and the ByeDPI engine into
# ui-desktop/src-tauri/resources so the desktop app can bundle them. These
# binaries are not checked in; run this before building the desktop bundle.
# Pinned versions keep builds reproducible.
$ErrorActionPreference = "Stop"

$singboxVersion = "1.13.13"
$wintunVersion = "0.14.1"
# ByeDPI, the DPI-bypass engine. Upstream tags the release `v0.17.3` but names
# its archives `byedpi-17.3-*`, so the archive name is derived from the tag
# instead of being a second literal that can fall out of step on a bump.
$byedpiVersion = "0.17.3"
$byedpiAsset = $byedpiVersion -replace '^0\.', ''

# SHA-256 pins for every artifact fetched below. These downloads are bundled
# verbatim into the signed release, so a swapped upstream artifact -- a poisoned
# GitHub release, a cache-poisoned raw.githubusercontent.com, or a
# TLS-intercepting build box -- would otherwise be signed and shipped as a
# privileged tunnel binary. Each fetch is checked against its pin and the script
# aborts on any mismatch, so a tampered artifact never reaches the bundle.
#
# The .srs pins are the same files the bash script fetches, so their digests are
# byte-for-byte identical to the ones in scripts/fetch-resources.sh; the sing-box
# pin here is the Windows build and differs by platform.
#
# To refresh on a version/rule-set bump: bump the version, download the same URLs
# these variables feed (the GitHub windows zip, the wintun zip, the ByeDPI zip and
# the raw .srs files), run
#   Get-FileHash -Algorithm SHA256 <file>
# on each, and paste the digests here. The .srs sets are rebuilt periodically on
# the SagerNet `rule-set` branch, so their digests move even without a version
# change -- re-pin them whenever the bundled copies are refreshed. Mirror every
# change into scripts/fetch-resources.sh (identical .srs digests; darwin sing-box
# and linux ByeDPI digests for that script).
$singboxSha256 = "aea1fa983134a2e2d0600581d1178e98bd6bb93ae12ad8c333eaacae68a1694c"
$wintunSha256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
$byedpiSha256 = "70d2c94147193cb915f9c6eb5144b8d404dacbcfa90bda2383b6b211afafa456"
$geoipRuSha256 = "8bc18433e5d5b0644ba2a9ff74cd03428ba4f4e388b3c409f182de930e3c3170"
$geositeRuSha256 = "3fb41849eefac86a4e65a86da3b868ecd40512e4d3f097ee325474f4cd401f76"
$geositeAdsSha256 = "ca44c97fce76f4f889e08bbc28e80d497a43239328e09f760129844057a2780a"

# Verify-Sha256 aborts the build unless $Path hashes to the pinned $Expected
# digest. A mismatch means the fetched artifact is not the one these pins were cut
# against, so it must never be signed into a release -- treat it as fatal.
function Verify-Sha256($Path, $Expected) {
    $actual = (Get-FileHash -Algorithm SHA256 -Path $Path).Hash.ToLower()
    if ($actual -ne $Expected.ToLower()) {
        Write-Error "checksum mismatch for $Path`n  expected $Expected`n  actual   $actual`nrefusing to bundle an unverified artifact; see the SHA-256 pins in this script"
    }
}

$root = Split-Path -Parent $PSScriptRoot
$dest = Join-Path $root "ui-desktop\src-tauri\resources"
New-Item -ItemType Directory -Force -Path $dest | Out-Null

$singboxZip = Join-Path $env:TEMP "sing-box-$singboxVersion.zip"
$singboxUrl = "https://github.com/SagerNet/sing-box/releases/download/v$singboxVersion/sing-box-$singboxVersion-windows-amd64.zip"
curl.exe -L --retry 12 --retry-all-errors --retry-delay 2 -C - -o $singboxZip $singboxUrl
Verify-Sha256 $singboxZip $singboxSha256
$singboxOut = Join-Path $env:TEMP "sing-box-$singboxVersion"
Expand-Archive -Path $singboxZip -DestinationPath $singboxOut -Force
$exe = Get-ChildItem $singboxOut -Recurse -Filter sing-box.exe | Select-Object -First 1
Copy-Item $exe.FullName (Join-Path $dest "sing-box.exe") -Force

$wintunZip = Join-Path $env:TEMP "wintun-$wintunVersion.zip"
$wintunUrl = "https://www.wintun.net/builds/wintun-$wintunVersion.zip"
curl.exe -L --retry 12 --retry-all-errors --retry-delay 2 -C - -o $wintunZip $wintunUrl
Verify-Sha256 $wintunZip $wintunSha256
$wintunOut = Join-Path $env:TEMP "wintun-$wintunVersion"
Expand-Archive -Path $wintunZip -DestinationPath $wintunOut -Force
$dll = Get-ChildItem $wintunOut -Recurse -Filter wintun.dll | Where-Object { $_.FullName -match "amd64" } | Select-Object -First 1
Copy-Item $dll.FullName (Join-Path $dest "wintun.dll") -Force

# ByeDPI (ciadpi.exe), the DPI-bypass engine. It is an ordinary userspace SOCKS5
# proxy -- no driver and no elevation, unlike the tunnel -- that the core spawns
# beside sing-box when the bypass is on, so it is bundled into the same resources
# directory and resolved the same way sing-box is. Only the executable is taken:
# the archive also carries .bat helpers for installing it as a Windows service,
# which Tenebra does not use because the core owns the process lifecycle.
$byedpiZip = Join-Path $env:TEMP "byedpi-$byedpiVersion.zip"
$byedpiUrl = "https://github.com/hufrea/byedpi/releases/download/v$byedpiVersion/byedpi-$byedpiAsset-x86_64-w64.zip"
curl.exe -L --retry 12 --retry-all-errors --retry-delay 2 -C - -o $byedpiZip $byedpiUrl
Verify-Sha256 $byedpiZip $byedpiSha256
$byedpiOut = Join-Path $env:TEMP "byedpi-$byedpiVersion"
Expand-Archive -Path $byedpiZip -DestinationPath $byedpiOut -Force
$ciadpi = Get-ChildItem $byedpiOut -Recurse -Filter ciadpi.exe | Select-Object -First 1
Copy-Item $ciadpi.FullName (Join-Path $dest "ciadpi.exe") -Force

# RU geo rule-sets, shipped locally so smart routing loads them from disk instead
# of downloading them from GitHub at startup (the download blocks sing-box for
# ~10s when raw.githubusercontent.com is throttled). These are the official
# SagerNet rule-sets, pinned to an immutable commit rather than the rolling
# `rule-set` branch — the branch is regenerated daily, so a branch URL would drift
# out from under the SHA-256 pins below and break the build on the next upstream
# rebuild (which is exactly what happened to geosite-ads). Bump the commit + SHA
# together to update a rule-set on purpose.
$geoipUrl = "https://raw.githubusercontent.com/SagerNet/sing-geoip/a508a0a09d30111e0ab5a0d9a3de1aff832d72b4/geoip-ru.srs"
curl.exe -L --retry 12 --retry-all-errors --retry-delay 2 -o (Join-Path $dest "geoip-ru.srs") $geoipUrl
Verify-Sha256 (Join-Path $dest "geoip-ru.srs") $geoipRuSha256
$geositeUrl = "https://raw.githubusercontent.com/SagerNet/sing-geosite/02b7bc85184c7fa94ccdfe9a35b7f4a169b28b4d/geosite-category-ru.srs"
curl.exe -L --retry 12 --retry-all-errors --retry-delay 2 -o (Join-Path $dest "geosite-ru.srs") $geositeUrl
Verify-Sha256 (Join-Path $dest "geosite-ru.srs") $geositeRuSha256

# Ad/tracker blocklist for the opt-in DNS ad-blocker. Same SagerNet source and
# license as the RU geosite set (compiled from the v2fly community domain list).
# It is bundled and loaded strictly as a LOCAL rule-set — never fetched at
# runtime — so it can never reintroduce the startup freeze a remote rule-set caused.
$geositeAdsUrl = "https://raw.githubusercontent.com/SagerNet/sing-geosite/02b7bc85184c7fa94ccdfe9a35b7f4a169b28b4d/geosite-category-ads-all.srs"
curl.exe -L --retry 12 --retry-all-errors --retry-delay 2 -o (Join-Path $dest "geosite-ads.srs") $geositeAdsUrl
Verify-Sha256 (Join-Path $dest "geosite-ads.srs") $geositeAdsSha256

Write-Host "Fetched sing-box $singboxVersion, wintun $wintunVersion, ByeDPI $byedpiVersion, and RU + ads rule-sets into $dest"
