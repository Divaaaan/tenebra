param([switch]$PolicyOnly)

# Embedded as constant source in the uninstaller, never loaded from an installed
# or temporary script file. -PolicyOnly exposes only pure path/ACL checks to CI.
$ErrorActionPreference = 'Stop'

function Get-TenebraCleanupPathChain([string]$Candidate) {
    if ($Candidate -notmatch '^[A-Za-z]:\\' -or $Candidate.Substring(2).Contains(':') -or $Candidate.Contains([char]0)) {
        throw 'Protection cleanup requires an absolute local installed core path.'
    }
    $core = [IO.Path]::GetFullPath($Candidate)
    if ($core -cne $Candidate -or [IO.Path]::GetFileName($core) -ine 'tenebra-core.exe') {
        throw 'Protection cleanup executable path is ambiguous.'
    }
    $chain = @()
    for ($path = $core; $path; $path = [IO.Path]::GetDirectoryName($path)) {
        $name = [IO.Path]::GetFileName($path)
        if ($name -and ($name.TrimEnd(' ', '.') -cne $name)) { throw 'Ambiguous cleanup path component.' }
        $chain = @($path) + $chain
    }
    return $chain
}

function Assert-TenebraCleanupAcl([Security.AccessControl.RawSecurityDescriptor]$Descriptor, [bool]$File) {
    $trusted = @('S-1-5-18', 'S-1-5-32-544', 'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464')
    if (!$Descriptor.Owner -or $Descriptor.Owner.Value -notin $trusted -or $null -eq $Descriptor.DiscretionaryAcl) {
        throw 'Cleanup path needs administrator ownership and a restrictive DACL.'
    }
    # Ancestors may allow creation of unrelated children (the volume root does).
    # Mutation, delete-child, ACL/owner changes and all writes to the EXE are denied.
    $mutation = 0x520D0150L
    if ($File) { $mutation = $mutation -bor 6 }
    foreach ($ace in $Descriptor.DiscretionaryAcl) {
        if (([int]$ace.AceFlags -band [int][Security.AccessControl.AceFlags]::InheritOnly) -ne 0) { continue }
        if ($ace -isnot [Security.AccessControl.CommonAce]) { throw 'Unsupported cleanup path ACL entry.' }
        if ($ace.AceQualifier -eq [Security.AccessControl.AceQualifier]::AccessDenied) { continue }
        if ($ace.AceQualifier -ne [Security.AccessControl.AceQualifier]::AccessAllowed -or $ace.IsCallback) { throw 'Unsupported cleanup path ACL entry.' }
        if ($ace.SecurityIdentifier.Value -notin $trusted -and (([long]$ace.AccessMask -band $mutation) -ne 0)) {
            throw 'Cleanup path is writable by a non-administrator.'
        }
    }
}

function Assert-TenebraCleanupImage([string[]]$Chain) {
    foreach ($path in $Chain) {
        $item = Get-Item -LiteralPath $path -Force
        if (([int]$item.Attributes -band [int][IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Cleanup path contains a reparse point.' }
        $isFile = $path -ceq $Chain[-1]
        if ($item.PSIsContainer -eq $isFile) { throw 'Cleanup path has the wrong file type.' }
        $acl = Get-Acl -LiteralPath $path
        $descriptor = [Security.AccessControl.RawSecurityDescriptor]::new($acl.GetSecurityDescriptorBinaryForm(), 0)
        Assert-TenebraCleanupAcl $descriptor $isFile
    }
}

if ($PolicyOnly) { return }

$image = $null
$process = $null
try {
    $chain = @(Get-TenebraCleanupPathChain ([Environment]::GetEnvironmentVariable('TENEBRA_RELEASE_CORE')))
    if ([IO.DriveInfo]::new($chain[0]).DriveType -ne [IO.DriveType]::Fixed) { throw 'Cleanup image must be on a local fixed drive.' }
    Assert-TenebraCleanupImage $chain
    $core = $chain[-1]
    # Hold a non-inheritable handle denying writes/delete while launching the
    # checked image. Existing hostile write handles cause this open to fail.
    $image = [IO.File]::Open($core, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $core
    $start.WorkingDirectory = [IO.Path]::GetDirectoryName($core)
    $start.Arguments = '--release-host-protection'
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $process = [Diagnostics.Process]::Start($start)
    if (!$process.WaitForExit(20000)) {
        $process.Kill()
        $null = $process.WaitForExit(1000)
        throw 'Owned host-protection cleanup timed out; repair before uninstalling.'
    }
    if ($process.ExitCode -ne 0) { throw "Owned host-protection cleanup was not confirmed (exit $($process.ExitCode)); repair with a protection-aware installer." }
    exit 0
} catch {
    [Console]::Error.WriteLine("Tenebra protection cleanup refused: $($_.Exception.Message)")
    exit 1
} finally {
    if ($process) { $process.Dispose() }
    if ($image) { $image.Dispose() }
}
