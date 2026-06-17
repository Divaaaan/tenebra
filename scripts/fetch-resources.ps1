# Fetches the sing-box binary and wintun.dll into ui-desktop/src-tauri/resources
# so the desktop app can bundle them. These binaries are not checked in; run this
# before building the desktop bundle. Pinned versions keep builds reproducible.
$ErrorActionPreference = "Stop"

$singboxVersion = "1.13.13"
$wintunVersion = "0.14.1"

$root = Split-Path -Parent $PSScriptRoot
$dest = Join-Path $root "ui-desktop\src-tauri\resources"
New-Item -ItemType Directory -Force -Path $dest | Out-Null

$singboxZip = Join-Path $env:TEMP "sing-box-$singboxVersion.zip"
$singboxUrl = "https://github.com/SagerNet/sing-box/releases/download/v$singboxVersion/sing-box-$singboxVersion-windows-amd64.zip"
curl.exe -L --retry 12 --retry-all-errors --retry-delay 2 -C - -o $singboxZip $singboxUrl
$singboxOut = Join-Path $env:TEMP "sing-box-$singboxVersion"
Expand-Archive -Path $singboxZip -DestinationPath $singboxOut -Force
$exe = Get-ChildItem $singboxOut -Recurse -Filter sing-box.exe | Select-Object -First 1
Copy-Item $exe.FullName (Join-Path $dest "sing-box.exe") -Force

$wintunZip = Join-Path $env:TEMP "wintun-$wintunVersion.zip"
$wintunUrl = "https://www.wintun.net/builds/wintun-$wintunVersion.zip"
curl.exe -L --retry 12 --retry-all-errors --retry-delay 2 -C - -o $wintunZip $wintunUrl
$wintunOut = Join-Path $env:TEMP "wintun-$wintunVersion"
Expand-Archive -Path $wintunZip -DestinationPath $wintunOut -Force
$dll = Get-ChildItem $wintunOut -Recurse -Filter wintun.dll | Where-Object { $_.FullName -match "amd64" } | Select-Object -First 1
Copy-Item $dll.FullName (Join-Path $dest "wintun.dll") -Force

Write-Host "Fetched sing-box $singboxVersion and wintun $wintunVersion into $dest"
