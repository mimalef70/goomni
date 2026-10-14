<#
.SYNOPSIS
    Removes the GoOmni Windows service. The install folder (database with the
    logged-in Bale session, master.key, logs) is kept unless -DeleteData is given.
#>
param(
    [string]$InstallDir = 'C:\GoOmni',
    [string]$ServiceName = 'GoOmni',
    [switch]$DeleteData
)
$ErrorActionPreference = 'Stop'
$principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script from an elevated (Administrator) PowerShell.'
}
if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Stop-Service -Name $ServiceName -ErrorAction SilentlyContinue
    & sc.exe delete $ServiceName | Out-Null
    Write-Host "Service $ServiceName removed."
}
if ($DeleteData) {
    Remove-Item -Recurse -Force $InstallDir
    Write-Host "$InstallDir deleted (the Bale session is gone; a new install needs a new login)."
} else {
    Write-Host "$InstallDir kept (database, master.key, logs)."
}
