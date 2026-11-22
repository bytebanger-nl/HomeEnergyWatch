# Runs inside Windows Sandbox (see run.ps1): installs the newest
# dist\hwlocal-setup-*.exe silently, checks the service and the web server,
# runs the setup again as an update, then uninstalls. Everything is written
# to C:\hwlocal\out, which is dist\sandbox-out on the host.
$out = 'C:\hwlocal\out'
Start-Transcript -Path "$out\transcript.txt" -Force | Out-Null
$ProgressPreference = 'SilentlyContinue'

function Step($name) { Write-Host ""; Write-Host "=== $name ===" }

function Check-Gateway {
    sc.exe query hwlocal
    sc.exe qc hwlocal
    $ok = $false
    for ($i = 0; $i -lt 30 -and -not $ok; $i++) {
        try {
            $r = Invoke-WebRequest -Uri 'http://localhost:8080/api/info' -UseBasicParsing -TimeoutSec 3
            Write-Host "api/info $($r.StatusCode): $($r.Content)"
            $ok = $true
        } catch { Start-Sleep -Seconds 1 }
    }
    if (-not $ok) { Write-Host "FAIL: gateway not reachable on :8080" }
    return $ok
}

# Runs an installer or uninstaller silently; a hang counts as failure.
function Run-Silent($exe, $log) {
    $p = Start-Process $exe -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/LOG=$log" -PassThru
    if (-not $p.WaitForExit(180000)) {
        Write-Host "FAIL: $exe still running after 3 minutes; killed"
        $p.Kill()
        return -1
    }
    Write-Host "exit $($p.ExitCode)"
    return $p.ExitCode
}

$setup = Get-ChildItem 'C:\hwlocal\dist\hwlocal-setup-*.exe' | Sort-Object LastWriteTime | Select-Object -Last 1
Write-Host "setup: $($setup.Name)"
$result = @{}

Step 'install while port 8080 is taken (must be refused)'
$blocker = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Any, 8080)
$blocker.Start()
$rc = Run-Silent $setup.FullName "$out\setup-busy.log"
$blocker.Stop()
$svc = Get-Service hwlocal -ErrorAction SilentlyContinue
Write-Host "service after refused install: $(if ($svc) { $svc.Status } else { 'none' })"
$result.busyport = ($rc -gt 0) -and ($null -eq $svc) -and -not (Test-Path 'C:\Program Files\hwlocal\hwlocal.exe')
if ($svc) { # remove what the refused attempt left, so the fresh install is fresh
    sc.exe delete hwlocal | Out-Null
    Start-Sleep -Seconds 2
}

Step 'fresh install'
$rc = Run-Silent $setup.FullName "$out\setup-install.log"
$result.install = ($rc -eq 0) -and (Check-Gateway)
icacls 'C:\ProgramData\hwlocal'
netsh advfirewall firewall show rule name=hwlocal
Get-ChildItem 'C:\ProgramData\hwlocal' | Format-Table Name, Length

Step 'update (same setup again)'
$rc = Run-Silent $setup.FullName "$out\setup-update.log"
$result.update = ($rc -eq 0) -and (Check-Gateway)
Copy-Item 'C:\ProgramData\hwlocal\hwlocal.log' "$out\hwlocal.log" -ErrorAction SilentlyContinue

Step 'uninstall'
$rc = Run-Silent 'C:\Program Files\hwlocal\unins000.exe' "$out\uninstall.log"
Start-Sleep -Seconds 3 # the uninstaller finishes in a copy of itself
sc.exe query hwlocal
$fw = netsh advfirewall firewall show rule name=hwlocal
$result.uninstall = (-not (Get-Service hwlocal -ErrorAction SilentlyContinue)) -and ($fw -match 'No rules|Geen regels') -and (Test-Path 'C:\ProgramData\hwlocal\hwlocal.db')
Write-Host "data kept: $(Test-Path 'C:\ProgramData\hwlocal\hwlocal.db')"

Step 'result'
$result.GetEnumerator() | ForEach-Object { Write-Host ("{0}: {1}" -f $_.Key, $(if ($_.Value) { 'PASS' } else { 'FAIL' })) }
Stop-Transcript | Out-Null
$result.GetEnumerator() | ForEach-Object { "{0}={1}" -f $_.Key, $_.Value } | Set-Content "$out\done.txt"
