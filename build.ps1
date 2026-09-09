# build.ps1 - Windows equivalent of `make build` for preflight.
# Usage:
#   .\build.ps1            production build (trimpath + stripped symbols, ~half size)
#   .\build.ps1 -Debug     plain build, identical to `make build`
param(
    [switch]$Debug,
    [string]$Output = "preflight.exe"
)

$ErrorActionPreference = "Stop"

$args = @("build")

if (-not $Debug) {
    $args += @("-trimpath", "-ldflags", "-s -w")
}

$args += @("-o", $Output, ".\cmd\preflight")

Write-Host "Building $Output ..." -ForegroundColor Cyan
& go @args

if ($LASTEXITCODE -ne 0) {
    Write-Error "Build failed with exit code $LASTEXITCODE"
    exit $LASTEXITCODE
}

$file = Get-Item $Output
$sizeKB = [math]::Round($file.Length / 1KB)

# pfl alias shim: forwards all args to preflight.exe (cmd + PowerShell)
$exeName = Split-Path $Output -Leaf
$outDir = Split-Path $Output -Parent
if (-not $outDir) { $outDir = "." }
$aliasPath = Join-Path $outDir "pfl.cmd"
Set-Content -Path $aliasPath -Value @("@echo off", "`"%~dp0$exeName`" %*") -Encoding Ascii
Write-Host "Alias: $aliasPath -> $exeName" -ForegroundColor Cyan
Write-Host "OK: $Output ($sizeKB KB) - run with .\$exeName -workers 8 (or just pfl)" -ForegroundColor Green
