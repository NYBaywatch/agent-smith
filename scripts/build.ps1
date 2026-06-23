# Builds Agent Smith for Windows (amd64).
#   .\scripts\build.ps1            # builds dist\agent-smith.exe (GUI) + agent-smith-cli.exe
#   .\scripts\build.ps1 -Version v0.1.0
param(
    [string]$Version = "dev"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    New-Item -ItemType Directory -Force -Path dist | Out-Null

    Write-Host "Building GUI binary (dist\agent-smith.exe)…"
    go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$Version" -o dist\agent-smith.exe .\cmd\agent-smith
    if ($LASTEXITCODE -ne 0) { throw "GUI build failed" }

    Write-Host "Building console binary (dist\agent-smith-cli.exe)…"
    go build -trimpath -ldflags "-s -w -X main.version=$Version" -o dist\agent-smith-cli.exe .\cmd\agent-smith
    if ($LASTEXITCODE -ne 0) { throw "console build failed" }

    Write-Host "Done. Binaries in $root\dist"
    Get-ChildItem dist | Format-Table Name, Length -AutoSize
}
finally {
    Pop-Location
}
