$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$node = 'D:\DevTools\node-v24.22.1-win-x64'
$go = 'D:\LiveStream\baseline-20261106\tools\go\bin'
if (Test-Path (Join-Path $node 'npm.cmd')) { $env:PATH = "$node;$env:PATH" }
if (Test-Path (Join-Path $go 'go.exe')) { $env:PATH = "$go;$env:PATH" }
$env:GOMAXPROCS = '2'
$env:GOTOOLCHAIN = 'local'
if ([string]::IsNullOrWhiteSpace($env:GOCACHE) -and (Test-Path -LiteralPath 'D:\LiveStream\baseline-20261106\tools\go-cache')) { $env:GOCACHE = 'D:\LiveStream\baseline-20261106\tools\go-cache' }
if ([string]::IsNullOrWhiteSpace($env:GOMODCACHE) -and (Test-Path -LiteralPath 'D:\LiveStream\baseline-20261106\tools\go-mod-cache')) { $env:GOMODCACHE = 'D:\LiveStream\baseline-20261106\tools\go-mod-cache' }
$bin = Join-Path $repo 'bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null
Push-Location (Join-Path $repo 'web')
try {
    if (-not (Test-Path 'node_modules')) { & npm.cmd ci; if ($LASTEXITCODE -ne 0) { throw 'npm ci failed.' } }
    & npm.cmd run build
    if ($LASTEXITCODE -ne 0) { throw 'Web build failed.' }
} finally { Pop-Location }
Push-Location $repo
try {
    foreach ($name in @('launcher', 'server', 'consumer')) {
        & go.exe build -p 1 -o (Join-Path $bin "$name.exe") "./cmd/$name"
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $name" }
    }
} finally { Pop-Location }
Write-Host 'Build complete. No services were started.'
