$ErrorActionPreference = 'Stop'
$repo = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$configPath = Join-Path $repo '.local\launcher.json'
$secretPath = Join-Path $repo '.local\mysql-dsn.xml'
$stateDir = Join-Path $repo '.local\runtime'
$launcher = Join-Path $repo 'bin\launcher.exe'

if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    throw 'Run scripts\setup-docker.ps1 explicitly before initializing Docker data.'
}
$config = [IO.File]::ReadAllText($configPath) | ConvertFrom-Json
if ($config.version -ne 1 -or $config.backend -ne 'docker' -or $null -eq $config.docker) {
    throw 'Docker backend configuration is required. Existing backend configuration was preserved.'
}
if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) {
    throw 'The host launcher is missing. Run scripts\build-docker.ps1 before initializing data.'
}
if (-not (Test-Path -LiteralPath $secretPath -PathType Leaf)) {
    throw 'The saved Docker host DSN is missing. Run scripts\setup-docker.ps1 explicitly.'
}

$hadDSN = Test-Path Env:DANMAKUFLOW_MYSQL_DSN
$previousDSN = $env:DANMAKUFLOW_MYSQL_DSN
$ptr = [IntPtr]::Zero
try {
    # Always use the DPAPI-protected Docker host DSN, never an inherited DSN
    # that might refer to a different database. The launcher validates loopback.
    $secure = Import-Clixml -LiteralPath $secretPath
    if ($secure -isnot [Security.SecureString]) { throw 'The saved Docker DSN is invalid.' }
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    $env:DANMAKUFLOW_MYSQL_DSN = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
    & $launcher -config $configPath -state-dir $stateDir -initialize-docker
    if ($LASTEXITCODE -ne 0) {
        throw 'Docker initialization did not complete. Open the local page to inspect logs and stop recorded services before retrying.'
    }
} finally {
    if ($ptr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
    if ($hadDSN) {
        $env:DANMAKUFLOW_MYSQL_DSN = $previousDSN
    } else {
        Remove-Item Env:DANMAKUFLOW_MYSQL_DSN -ErrorAction SilentlyContinue
    }
    $previousDSN = $null
    $secure = $null
}
