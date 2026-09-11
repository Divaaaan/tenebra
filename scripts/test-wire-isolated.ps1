param(
  [Parameter(Mandatory=$true)][string]$DependencyDirectory,
  [Parameter(Mandatory=$true)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
# Build the real protocol implementation without Tauri/WebView or OS pipes.
# Only unrelated event/backend mappings are excluded. No network or daemon runs.
$repoRoot = Split-Path $PSScriptRoot -Parent
$wireSource = Get-Content -LiteralPath (Join-Path $repoRoot 'ui-desktop/src-tauri/src/backend/wire.rs') -Raw
$prefix = $wireSource.Substring(0, $wireSource.IndexOf('/// Read the stream to EOF'))
$prefix = [regex]::Replace($prefix, '(?s)use super::\{.*?\};', '')
$start = $wireSource.IndexOf('fn fail_all_pending(')
$end = $wireSource.IndexOf('/// Forward a protocol event')
$helpers = $wireSource.Substring($start, $end - $start)
$testsPath = (Join-Path $repoRoot 'ui-desktop/src-tauri/src/backend/wire_deadline_tests.rs').Replace('\', '/')
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$harnessPath = Join-Path $OutputDirectory 'wire-isolated.rs'
[IO.File]::WriteAllText($harnessPath, $prefix + $helpers + "`n#[cfg(test)]`n#[path = `"$testsPath`"]`nmod deadline_tests;`n")
$jsonLib = Get-ChildItem -LiteralPath $DependencyDirectory -Filter 'libserde_json-*.rlib' | Sort-Object LastWriteTime -Descending | Select-Object -First 1
$serdeLib = Get-ChildItem -LiteralPath $DependencyDirectory -Filter 'libserde-*.rlib' | Sort-Object LastWriteTime -Descending | Select-Object -First 1
$exe = Join-Path $OutputDirectory 'wire-isolated.exe'
& rustc --edition 2021 --test $harnessPath -L "dependency=$DependencyDirectory" --extern "serde_json=$($jsonLib.FullName)" --extern "serde=$($serdeLib.FullName)" -o $exe
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& $exe --test-threads=1
exit $LASTEXITCODE
