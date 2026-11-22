# Builds the Windows installer: dist\hwlocal-setup-<version>.exe
# Needs Go and Inno Setup 6 (winget install JRSoftware.InnoSetup).
#
# Without -Version the version follows the same rules as scripts/version.sh:
# 0.1.N on the default branch (N = commits on its first-parent line, one per
# merged pull request; in the public repository counted from its v0.1.K
# tags), else 0.1.<N+1>-dev.<commit>; "-dirty" with
# uncommitted changes.
param([string]$Version = "", [string]$MainRef = "origin/HEAD")
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    if (-not $Version) {
        # Windows PowerShell turns git's stderr into an error under "Stop"
        $ErrorActionPreference = "Continue"
        git rev-parse -q --verify $MainRef *> $null
        if ($LASTEXITCODE -ne 0) { $MainRef = "origin/main" }
        $sha = git rev-parse --short HEAD
        # N for a commit: its first-parent commits, or, in the public
        # repository (scripts/publish.sh), the nearest v0.1.K tag plus the
        # commits after it
        function Get-Count([string]$Ref) {
            $tag = git describe --tags --match "v0.1.*" --abbrev=0 --first-parent $Ref 2>$null
            if ($LASTEXITCODE -eq 0 -and $tag -match '^v0\.1\.(\d+)$') {
                return [int]$Matches[1] + [int](git rev-list --count --first-parent "$tag..$Ref")
            }
            return [int](git rev-list --count --first-parent $Ref)
        }
        if ((git rev-parse HEAD) -eq (git rev-parse $MainRef)) {
            $Version = "0.1." + (Get-Count HEAD)
        } else {
            $base = git merge-base HEAD $MainRef
            $Version = "0.1.$((Get-Count $base) + 1)-dev.$sha"
        }
        if (git status --porcelain --untracked-files=no) { $Version += "-dirty" }
        $ErrorActionPreference = "Stop"
    }
    # Windows wants a numeric file version: 0.1.N.0 (0.0.0.0 when not numeric)
    $numeric = if ($Version -match '^(\d+)\.(\d+)\.(\d+)') { "$($Matches[1]).$($Matches[2]).$($Matches[3]).0" } else { "0.0.0.0" }

    $env:CGO_ENABLED = "0"; $env:GOOS = "windows"; $env:GOARCH = "amd64"
    go build -trimpath -ldflags "-s -w -X main.version=$Version" -o dist\hwlocal.exe .\cmd\hwlocal
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    $iscc = @(
        (Get-Command iscc -ErrorAction SilentlyContinue).Source,
        "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe",
        "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
        "$env:ProgramFiles\Inno Setup 6\ISCC.exe"
    ) | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
    if (-not $iscc) { throw "Inno Setup 6 not found: winget install JRSoftware.InnoSetup" }
    & $iscc /Qp "/DAppVersion=$Version" "/DNumericVersion=$numeric" installer\hwlocal.iss
    if ($LASTEXITCODE -ne 0) { throw "ISCC failed" }
    Write-Host "installer: $root\dist\hwlocal-setup-$Version.exe"
} finally { Pop-Location }
