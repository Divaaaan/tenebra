$ErrorActionPreference = 'Stop'

# Exercise the bootstrap from the actual NSIS wait command in fresh Windows
# PowerShell processes. A nameless ServiceController constructor opens no SCM
# handle and never queries or changes a host service.
$hooks = [IO.File]::ReadAllText((Join-Path $PSScriptRoot '../ui-desktop/src-tauri/installer-hooks.nsh'))
$pattern = 'try \{ (?<bootstrap>(?:Add-Type -AssemblyName System\.ServiceProcess -ErrorAction Stop; )?)\(New-Object System\.ServiceProcess\.ServiceController\(''tenebra''\)\)\.WaitForStatus\(\[System\.ServiceProcess\.ServiceControllerStatus\]::Stopped,\[TimeSpan\]::FromSeconds\(30\)\); exit 0 \} catch \{ exit 1 \}'
$matches = [regex]::Matches($hooks, $pattern)
if ($matches.Count -ne 1) { throw 'Expected one reviewed installer WaitForStatus command.' }
$bootstrap = $matches[0].Groups['bootstrap'].Value
$command = '$ErrorActionPreference = ''Stop''; ' + $bootstrap + '$controller = New-Object System.ServiceProcess.ServiceController; [void][System.ServiceProcess.ServiceControllerStatus]; $controller.Dispose(); Write-Output ''PASS: service types loaded without a service name'''
$encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($command))
$systemDirectory = if ([Environment]::Is64BitOperatingSystem -and -not [Environment]::Is64BitProcess) { 'Sysnative' } else { 'System32' }
$executables = @((Join-Path $env:SystemRoot "$systemDirectory/WindowsPowerShell/v1.0/powershell.exe"))
if ([Environment]::Is64BitOperatingSystem) {
    $executables += Join-Path $env:SystemRoot 'SysWOW64/WindowsPowerShell/v1.0/powershell.exe'
}
foreach ($executable in $executables) {
    $start = New-Object Diagnostics.ProcessStartInfo
    $start.FileName = $executable
    $start.Arguments = "-NoProfile -NonInteractive -OutputFormat Text -EncodedCommand $encoded"
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $child = New-Object Diagnostics.Process
    $child.StartInfo = $start
    try {
        if (-not $child.Start()) { throw 'Could not start the isolated bootstrap check.' }
        $null = $child.Handle
        $stdout = $child.StandardOutput.ReadToEndAsync()
        $stderr = $child.StandardError.ReadToEndAsync()
        if (-not $child.WaitForExit(15000)) {
            $child.Kill()
            throw 'Installer bootstrap check exceeded 15 seconds.'
        }
        if ($child.ExitCode -ne 0) { throw "Fresh $executable bootstrap failed: $($stderr.Result)" }
        if ($stdout.Result.Trim() -cne 'PASS: service types loaded without a service name') { throw 'Missing isolated bootstrap proof.' }
        Write-Output "PASS: fresh $executable loads installer service types without SCM access."
    } finally {
        $child.Dispose()
    }
}
