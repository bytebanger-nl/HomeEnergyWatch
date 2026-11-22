# Tests the installer in Windows Sandbox without touching this machine:
# maps dist\ (read-only) and this folder into the sandbox, runs test.ps1 at
# logon and collects the logs in dist\sandbox-out. Build the installer
# first (installer\build.ps1). Needs the Windows Sandbox feature.
# -Interactive skips the automatic test and opens C:\hwlocal\dist inside the
# sandbox instead, to click through the wizard by hand.
param([switch]$Interactive)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$dist = Join-Path $root "dist"
$out = Join-Path $dist "sandbox-out"
if (-not (Get-ChildItem "$dist\hwlocal-setup-*.exe" -ErrorAction SilentlyContinue)) {
    throw "no installer in $dist; run installer\build.ps1 first"
}
if (Test-Path $out) { Remove-Item -Recurse -Force $out }
New-Item -ItemType Directory $out | Out-Null
$command = 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\hwlocal\test\test.ps1'
if ($Interactive) { $command = 'explorer.exe C:\hwlocal\dist' }
$wsb = Join-Path $dist "hwlocal-test.wsb"
@"
<Configuration>
  <MappedFolders>
    <MappedFolder><HostFolder>$dist</HostFolder><SandboxFolder>C:\hwlocal\dist</SandboxFolder><ReadOnly>true</ReadOnly></MappedFolder>
    <MappedFolder><HostFolder>$PSScriptRoot</HostFolder><SandboxFolder>C:\hwlocal\test</SandboxFolder><ReadOnly>true</ReadOnly></MappedFolder>
    <MappedFolder><HostFolder>$out</HostFolder><SandboxFolder>C:\hwlocal\out</SandboxFolder><ReadOnly>false</ReadOnly></MappedFolder>
  </MappedFolders>
  <LogonCommand>
    <Command>$command</Command>
  </LogonCommand>
</Configuration>
"@ | Set-Content -Encoding UTF8 $wsb
Start-Process $wsb
Write-Host "sandbox started; results in $out (done.txt when finished)"
