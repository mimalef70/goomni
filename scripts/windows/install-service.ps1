<#
.SYNOPSIS
    Installs GoOmni as a Windows service (no Docker, no Linux server).

.DESCRIPTION
    Run from an elevated PowerShell next to goomni.exe (built with
    scripts\windows\build.ps1). It:
      1. copies goomni.exe to -InstallDir and, on a first install, runs `goomni init`
         there (private .env and master.key; the admin UI is switched off unless
         -WithUI is given, because build.ps1 embeds it only after `make ui-build`);
      2. registers the service under its own virtual account NT SERVICE\<name>,
         delayed automatic start, restart on failure;
      3. locks the folder down: Administrators and SYSTEM full control; the service
         account may read the program, .env and master.key, and write only
         storages\ and logs\.
    GoOmni listens on 127.0.0.1 only; no firewall rule is needed or added.
    Afterwards log in the Bale account once with
        cd <InstallDir>; .\goomni.exe login --device support --provider bale
    (it asks for the phone number and the code Bale sends).

.EXAMPLE
    .\install-service.ps1 -InstallDir C:\GoOmni -BaleWebClient
#>
param(
    [string]$InstallDir = 'C:\GoOmni',
    [string]$ServiceName = 'GoOmni',
    [int]$Port = 3000,
    # Use the verified public Bale web client identity (see GoOmni README); otherwise
    # set BALE_APP_ID / BALE_API_KEY in .env yourself before logging in.
    [switch]$BaleWebClient,
    # Keep the admin UI enabled (only for a goomni.exe built with the UI assets).
    [switch]$WithUI
)
$ErrorActionPreference = 'Stop'

$principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script from an elevated (Administrator) PowerShell.'
}
$source = Join-Path $PSScriptRoot 'goomni.exe'
if (-not (Test-Path $source)) { $source = Join-Path (Get-Location) 'goomni.exe' }
if (-not (Test-Path $source)) { throw 'goomni.exe was not found next to this script or in the current folder.' }

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing -and $existing.Status -ne 'Stopped') {
    Write-Host "Stopping $ServiceName for the update..."
    Stop-Service -Name $ServiceName
}

New-Item -ItemType Directory -Force $InstallDir | Out-Null
Copy-Item $source (Join-Path $InstallDir 'goomni.exe') -Force
Push-Location $InstallDir
try {
    if (-not (Test-Path '.env')) {
        $initArgs = @('init', '--dir', '.')
        if ($BaleWebClient) { $initArgs += '--bale-web-client' }
        & .\goomni.exe @initArgs
        if ($LASTEXITCODE -ne 0) { throw 'goomni init failed.' }
        # The port is set here; without -WithUI the admin UI is off (log in with `goomni login`).
        $envText = (Get-Content '.env' -Raw) -replace '(?m)^APP_PORT=.*$', "APP_PORT=$Port"
        if (-not $WithUI -and $envText -notmatch '(?m)^APP_UI_ENABLED=') { $envText = $envText.TrimEnd() + "`nAPP_UI_ENABLED=false`n" }
        [IO.File]::WriteAllText((Join-Path $InstallDir '.env'), $envText)   # keeps the private ACL set by init
    }
    New-Item -ItemType Directory -Force 'storages', 'logs' | Out-Null
} finally {
    Pop-Location
}

if (-not $existing) {
    $bin = '"' + (Join-Path $InstallDir 'goomni.exe') + '" rest'
    & sc.exe create $ServiceName binPath= $bin start= delayed-auto obj= "NT SERVICE\$ServiceName" DisplayName= 'GoOmni (Bale gateway)' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "sc.exe create failed ($LASTEXITCODE)." }
    & sc.exe description $ServiceName 'GoOmni messenger gateway (REST on 127.0.0.1).' | Out-Null
}
# restart after 1, 1 and 5 minutes; count failures over a day; also restart on an error exit
& sc.exe failure $ServiceName reset= 86400 actions= restart/60000/restart/60000/restart/300000 | Out-Null
& sc.exe failureflag $ServiceName 1 | Out-Null

function Invoke-Icacls([string[]]$IcaclsArgs) {
    & icacls @IcaclsArgs | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "icacls $($IcaclsArgs -join ' ') failed." }
}
# Folder rights. *S-1-5-32-544 = Administrators, *S-1-5-18 = SYSTEM.
$account = "NT SERVICE\$ServiceName"
Invoke-Icacls @($InstallDir, '/inheritance:r', '/grant:r', '*S-1-5-32-544:(OI)(CI)F', '*S-1-5-18:(OI)(CI)F', "${account}:(OI)(CI)RX")
foreach ($dir in 'storages', 'logs') {
    Invoke-Icacls @((Join-Path $InstallDir $dir), '/grant:r', "${account}:(OI)(CI)M")
}
# .env and master.key keep their protected list from `goomni init`; the service may only read them.
foreach ($file in '.env', 'master.key') {
    Invoke-Icacls @((Join-Path $InstallDir $file), '/grant', "${account}:R")
}

Start-Service -Name $ServiceName
Start-Sleep -Seconds 3
Get-Service -Name $ServiceName | Format-Table Name, Status, StartType
Write-Host "Log: $InstallDir\logs\goomni-service.log"
Write-Host "Next: cd $InstallDir; .\goomni.exe login --device support --provider bale"
