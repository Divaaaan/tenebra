$ErrorActionPreference = 'Stop'
. "$PSScriptRoot/../ui-desktop/src-tauri/installer-release-protection.ps1" -PolicyOnly

function Reject([scriptblock]$Action) {
    $rejected = $false
    try { & $Action } catch { $rejected = $true }
    if (!$rejected) { throw 'Unsafe cleanup policy input was accepted.' }
}

$paths = @(Get-TenebraCleanupPathChain 'C:\Program Files\Tenebra\tenebra-core.exe')
if ($paths.Count -ne 4 -or $paths[0] -cne 'C:\' -or $paths[3] -cne 'C:\Program Files\Tenebra\tenebra-core.exe') { throw 'Cleanup path chain is not rooted and ordered.' }
foreach ($path in @('', 'C:tenebra-core.exe', '\\host\share\tenebra-core.exe', 'C:\Tenebra\..\tenebra-core.exe', 'C:\Tenebra\other.exe', 'C:\Tenebra\tenebra-core.exe:stream', 'C:\Tenebra.\tenebra-core.exe')) {
    Reject { Get-TenebraCleanupPathChain $path }
}

$trusted = [Security.AccessControl.RawSecurityDescriptor]::new('O:BAG:BAD:(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)')
Assert-TenebraCleanupAcl $trusted $true
foreach ($sddl in @('O:BUG:BUD:(A;;FA;;;BU)', 'O:BAG:BAD:NO_ACCESS_CONTROL', 'O:BAG:BAD:(A;;FA;;;SY)(A;;FW;;;BU)', 'O:BAG:BAD:(A;;FA;;;SY)(A;;WD;;;BU)', 'O:BAG:BAD:(A;;FA;;;SY)(A;;WO;;;BU)')) {
    $bad = [Security.AccessControl.RawSecurityDescriptor]::new($sddl)
    Reject { Assert-TenebraCleanupAcl $bad $true }
}
$createChild = [Security.AccessControl.RawSecurityDescriptor]::new('O:BAG:BAD:(A;;FA;;;BA)(A;;0x6;;;BU)')
Assert-TenebraCleanupAcl $createChild $false
Reject { Assert-TenebraCleanupAcl $createChild $true }
Write-Output 'PASS: pure uninstall path and ACL policy; no files, services, processes or WFP objects opened.'
