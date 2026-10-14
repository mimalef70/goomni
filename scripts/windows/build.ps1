<#
.SYNOPSIS
    Builds goomni.exe for Windows (amd64), the same way the Docker image is built:
    pure Go (CGO off) with the portable SQLite driver (-tags purego).

.DESCRIPTION
    Needs only the Go toolchain named in src\go.mod (no C compiler, no Node: the
    admin UI is not embedded; the service runs with APP_UI_ENABLED=false and the
    Bale account is logged in with `goomni login`). Go is used only here; the
    server that runs GoOmni needs nothing but the resulting goomni.exe.
    Output: bin\windows\goomni.exe plus the install scripts, ready to copy.

.EXAMPLE
    .\scripts\windows\build.ps1 -Go D:\_tools\go1.26.9\go\bin\go.exe -Test
#>
param(
    [string]$Go = 'go',
    [switch]$Test
)
# Go reports progress on stderr, which Windows PowerShell turns into errors under
# 'Stop': success is judged by exit codes instead.
$ErrorActionPreference = 'Continue'
$root = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$out = Join-Path $root 'bin\windows'
New-Item -ItemType Directory -Force $out | Out-Null
# set for this build only; the caller's values are restored at the end
$goEnv = @{ CGO_ENABLED = '0'; GOOS = 'windows'; GOARCH = 'amd64'; GOTOOLCHAIN = 'local' }
$saved = @{}
foreach ($k in $goEnv.Keys) {
    $saved[$k] = [Environment]::GetEnvironmentVariable($k, 'Process')
    [Environment]::SetEnvironmentVariable($k, $goEnv[$k], 'Process')
}
Push-Location (Join-Path $root 'src')
try {
    if ($Test) {
        & $Go test -tags purego -count=1 ./...
        if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    }
    & $Go build -tags purego -trimpath -ldflags='-s -w' -o (Join-Path $out 'goomni.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
} finally {
    Pop-Location
    foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k], 'Process') }
}
Copy-Item (Join-Path $PSScriptRoot 'install-service.ps1'), (Join-Path $PSScriptRoot 'uninstall-service.ps1') $out -Force
Get-FileHash (Join-Path $out 'goomni.exe') -Algorithm SHA256 | Format-List Path, Hash
