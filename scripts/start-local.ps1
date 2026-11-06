$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$local = Join-Path $repo '.local'
$config = Join-Path $local 'launcher.json'
$secretPath = Join-Path $local 'mysql-dsn.xml'
$exe = Join-Path $repo 'bin\launcher.exe'
$web = Join-Path $repo 'web\dist'
$url = 'http://127.0.0.1:4174'
try {
    if (-not (Test-Path -LiteralPath $config) -or ((-not (Test-Path -LiteralPath $secretPath)) -and [string]::IsNullOrWhiteSpace($env:DANMAKUFLOW_MYSQL_DSN))) {
        & (Join-Path $PSScriptRoot 'setup-local.ps1')
    }
    foreach ($path in @($exe, (Join-Path $repo 'bin\server.exe'), (Join-Path $repo 'bin\consumer.exe'), (Join-Path $web 'index.html'))) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'Build artifacts are missing. Run scripts\build-local.ps1 first.' }
    }
    function Get-Session {
        try { return Invoke-RestMethod -Uri "$url/api/local/session" -TimeoutSec 2 -ErrorAction Stop } catch { return $null }
    }
    function Check-Session($session) {
        if ($session.kind -ne 'danmakuflow-launcher' -or [string]::IsNullOrWhiteSpace($session.configPath) -or -not [string]::Equals([IO.Path]::GetFullPath($session.configPath), [IO.Path]::GetFullPath($config), [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Port 4174 is occupied by an unrecognized launcher or a different configuration. Close it manually and retry.'
        }
    }
    $session = Get-Session
    if ($null -eq $session) {
        $oldDSN = [Environment]::GetEnvironmentVariable('DANMAKUFLOW_MYSQL_DSN', 'Process')
        $ptr = [IntPtr]::Zero
        try {
            if ([string]::IsNullOrWhiteSpace($oldDSN)) {
                $secure = Import-Clixml -LiteralPath $secretPath
                if ($secure -isnot [Security.SecureString]) { throw 'Saved DSN is invalid. Run scripts\setup-local.ps1.' }
                $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
                $env:DANMAKUFLOW_MYSQL_DSN = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
            }
            $state = Join-Path $local 'runtime'
            $arguments = @('-config', ('"' + $config + '"'), '-state-dir', ('"' + $state + '"'), '-web-dir', ('"' + $web + '"'))
            Start-Process -FilePath $exe -ArgumentList $arguments -WorkingDirectory $repo -WindowStyle Hidden -RedirectStandardOutput (Join-Path $local 'launcher.stdout.log') -RedirectStandardError (Join-Path $local 'launcher.stderr.log') | Out-Null
        } finally {
            if ($ptr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
            [Environment]::SetEnvironmentVariable('DANMAKUFLOW_MYSQL_DSN', $oldDSN, 'Process')
        }
        for ($i = 0; $i -lt 30 -and $null -eq $session; $i++) { Start-Sleep -Milliseconds 200; $session = Get-Session }
        if ($null -eq $session) { throw 'Launcher did not become available on port 4174. Check .local\launcher.stderr.log, port availability, and Windows application-control messages.' }
    }
    Check-Session $session
    Start-Process $url
} catch {
    # Preserve the original setup/startup error and its source location.
    throw
}
