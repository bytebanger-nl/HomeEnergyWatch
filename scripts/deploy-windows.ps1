# Runs on the Windows machine, started by scripts/deploy-windows.sh: replaces
# the exe of the hwlocal service with the hwlocal.exe next to this script and
# checks that the new one starts. The previous exe is kept as hwlocal.exe.old.
# If the new one does not start and did not migrate the database, the previous
# one is put back and started again.
# Written for Windows PowerShell 5.1; keep this file ASCII.
$ErrorActionPreference = "Stop"
# the log is UTF-8, and so is what goes back over SSH
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$new =Join-Path $PSScriptRoot "hwlocal.exe"

$svc = Get-CimInstance Win32_Service -Filter "Name='hwlocal'"
if (-not $svc) { throw "service hwlocal is not installed" }
$cmd = $svc.PathName
$exe = if ($cmd -match '^"([^"]+)"') { $Matches[1] } else { ($cmd -split ' ')[0] }

# a flag stored in the service command line: -name value, -name=value, quoted or not
function Flag($name, $default) {
    if ($cmd -match "\s--?$name[= ](?:`"([^`"]*)`"|(\S*))") {
        if ($Matches[1]) { return $Matches[1] } else { return $Matches[2] }
    }
    return $default
}
$data = Flag "data" (Join-Path $env:ProgramData "hwlocal")
$http = Flag "http" ":8080"
$log = Join-Path $data "hwlocal.log"

$oldVersion = (& $exe -version) -join " "
$newVersion = (& $new -version) -join " "
Write-Host "service: $exe"
Write-Host "  now: $oldVersion"
Write-Host "  new: $newVersion"

function Wait-Status($status, $seconds) {
    for ($i = 0; $i -lt $seconds; $i++) {
        if ((Get-Service hwlocal).Status -eq $status) { return $true }
        Start-Sleep -Seconds 1
    }
    return $false
}

# the log lines written since $since (lines without a time belong to the line before)
function Show-Log($since) {
    if (-not (Test-Path $log)) { Write-Host "  (no $log)"; return }
    $on = $false
    foreach ($line in Get-Content $log -Tail 300 -Encoding UTF8) {
        if ($line -match '^time=(\S+)') {
            try { $on = [DateTimeOffset]::Parse($Matches[1]) -ge $since } catch { }
        }
        if ($on) { Write-Host "  $line" }
    }
}

function Start-Hwlocal($path) {
    & $path service start
    if ($LASTEXITCODE -ne 0) { return $false }
    return (Wait-Status "Running" 15)
}

$started = [DateTimeOffset]::Now
Write-Host "stopping service"
& $exe service stop
if (-not (Wait-Status "Stopped" 30)) { throw "service did not stop" }

# the process may hold the file a moment after the service reports stopped
for ($i = 0; ; $i++) {
    try {
        Copy-Item $exe "$exe.old" -Force
        Copy-Item $new $exe -Force
        break
    } catch {
        if ($i -ge 10) { throw }
        Start-Sleep -Seconds 1
    }
}
Write-Host "replaced exe (previous kept as hwlocal.exe.old); starting service"

$ok = Start-Hwlocal $exe
if ($ok) {
    # give it time to open the database and start serving
    Start-Sleep -Seconds 8
    $ok = (Get-Service hwlocal).Status -eq "Running"
}
Write-Host "log since the stop:"
Show-Log $started

if ($ok -and $http) {
    $hostPart = $http.Substring(0, $http.LastIndexOf(":"))
    $port = $http.Substring($http.LastIndexOf(":") + 1)
    if (-not $hostPart -or $hostPart -eq "0.0.0.0" -or $hostPart -eq "[::]") { $hostPart = "127.0.0.1" }
    $url = "http://${hostPart}:$port/api/auth"
    try {
        $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 10 $url
        Write-Host "http check: $url -> $($r.StatusCode)"
    } catch {
        Write-Host "http check: $url failed: $($_.Exception.Message)"
        $ok = $false
    }
}

if ($ok) {
    Write-Host "OK: hwlocal runs $newVersion"
    exit 0
}

# a migration copies the database to hwlocal.db.v<N>.bak first; the previous
# exe refuses a database with a newer schema, so then it cannot simply go back
$migrated = Get-ChildItem $data -Filter "hwlocal.db.v*.bak" -ErrorAction SilentlyContinue |
    Where-Object { $_.LastWriteTime -ge $started.LocalDateTime }
if ($migrated) {
    Write-Host "FAILED, and the new version migrated the database (backup: $($migrated.Name -join ', '))."
    Write-Host "Not rolled back. To go back by hand: stop the service, copy hwlocal.exe.old over"
    Write-Host "hwlocal.exe, copy that backup over hwlocal.db (delete hwlocal.db-wal and -shm), start."
    exit 1
}
Write-Host "FAILED: putting the previous exe back"
& $exe service stop | Out-Null
Wait-Status "Stopped" 30 | Out-Null
Copy-Item "$exe.old" $exe -Force
if (Start-Hwlocal $exe) { Write-Host "previous version runs again: $oldVersion" }
else { Write-Host "previous version did not start either; see $log" }
exit 1
