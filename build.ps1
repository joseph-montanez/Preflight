# build.ps1 - Windows equivalent of `make build` for godeploy.
# Usage:
#   .\build.ps1            production build (trimpath + stripped symbols, ~half size)
#   .\build.ps1 -Debug     plain build, identical to `make build`
param(
    [switch]$Debug,
    [string]$Output = "godeploy.exe"
)

$ErrorActionPreference = "Stop"

$args = @("build")

if (-not $Debug) {
    $args += @("-trimpath", "-ldflags", "-s -w")
}

$args += @("-o", $Output, ".\cmd\godeploy")

Write-Host "Building $Output ..." -ForegroundColor Cyan
& go @args

if ($LASTEXITCODE -ne 0) {
    Write-Error "Build failed with exit code $LASTEXITCODE"
    exit $LASTEXITCODE
}

$file = Get-Item $Output
$sizeKB = [math]::Round($file.Length / 1KB)
Write-Host "OK: $Output ($sizeKB KB) - run with .\$Output -workers 8" -ForegroundColor Green
