param([string]$ComposeExecutable)

# Runs scripts with temporary configuration, fake tools and dummy credentials.
# Optional real Compose validation only parses a fixture; it starts no services.
$ErrorActionPreference = 'Stop'
$sourceScripts = $PSScriptRoot
$testBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$fixture = Join-Path $testBase ('danmakuflow-docker-test-' + [Guid]::NewGuid().ToString('N'))
$scripts = Join-Path $fixture 'scripts'
$password = 'test$money # ''single'' "double" \tail\'
$secure = ConvertTo-SecureString $password -AsPlainText -Force
$previousDSN = [Environment]::GetEnvironmentVariable('DANMAKUFLOW_MYSQL_DSN', 'Process')
$previousRootPassword = [Environment]::GetEnvironmentVariable('MYSQL_ROOT_PASSWORD', 'Process')
$global:DanmakuFlowDockerTest = @{ Root = $fixture; Calls = @(); BrowserOpened = $false; LauncherReady = $true; LauncherDSN = $null }

function Assert-PrivateAccess([string]$Path) {
    $acl = Get-Acl -LiteralPath $Path
    if (-not $acl.AreAccessRulesProtected) { throw "Private access inheritance was not protected: $Path" }
    $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    $expected = @([Security.Principal.WindowsIdentity]::GetCurrent().User.Value, 'S-1-5-18')
    if ($rules.Count -ne 2) { throw 'Unexpected access rule count.' }
    foreach ($rule in $rules) {
        if ($rule.IsInherited -or $rule.IdentityReference.Value -notin $expected -or
            $rule.AccessControlType -ne 'Allow' -or $rule.FileSystemRights -ne 'FullControl') {
            throw 'Unexpected access grant.'
        }
    }
}
function Read-SavedDSN([string]$Path) {
    $value = Import-Clixml -LiteralPath $Path
    if ($value -isnot [Security.SecureString]) { throw 'Saved DSN is not encrypted.' }
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($value)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
}
function Restore-TestEnvironment {
    foreach ($entry in @(@{ Name = 'DANMAKUFLOW_MYSQL_DSN'; Value = $previousDSN }, @{ Name = 'MYSQL_ROOT_PASSWORD'; Value = $previousRootPassword })) {
        if ($null -eq $entry.Value) {
            Remove-Item -LiteralPath ('Env:' + $entry.Name) -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($entry.Name, $entry.Value, 'Process')
        }
    }
}
function Invoke-RestMethod {
    param($Uri, $TimeoutSec, $ErrorAction)
    if (-not $global:DanmakuFlowDockerTest.LauncherReady) { throw 'Fixture launcher is not running.' }
    return @{ kind = 'danmakuflow-launcher'; configPath = (Join-Path $global:DanmakuFlowDockerTest.Root '.local\launcher.json') }
}
function Start-Process {
    param($FilePath, $ArgumentList, $WorkingDirectory, $WindowStyle, $RedirectStandardOutput, $RedirectStandardError)
    if ($FilePath -eq (Join-Path $global:DanmakuFlowDockerTest.Root 'bin\launcher.exe')) {
        $global:DanmakuFlowDockerTest.LauncherDSN = $env:DANMAKUFLOW_MYSQL_DSN
        $global:DanmakuFlowDockerTest.LauncherReady = $true
        return
    }
    if ($FilePath -ne 'http://127.0.0.1:4174') { throw 'Unexpected process startup in fixture.' }
    $global:DanmakuFlowDockerTest.BrowserOpened = $true
}

try {
    New-Item -ItemType Directory -Path $scripts -Force | Out-Null
    foreach ($name in @('setup-docker.ps1', 'build-docker.ps1', 'start-local.ps1')) {
        Copy-Item -LiteralPath (Join-Path $sourceScripts $name) -Destination (Join-Path $scripts $name)
    }
    $docker = Join-Path $fixture 'docker-fixture.ps1'
    [IO.File]::WriteAllText($docker, @'
$global:DanmakuFlowDockerTest.Calls += ,@($args)
if ($args[-1] -ne 'version' -and ((Test-Path Env:DANMAKUFLOW_MYSQL_DSN) -or (Test-Path Env:MYSQL_ROOT_PASSWORD))) { throw 'Host environment overrides the container env file.' }
$global:LASTEXITCODE = 0
if ($args[-1] -eq 'version') { Write-Output 'Docker Compose version fixture' }
'@)
    [IO.File]::WriteAllText((Join-Path $scripts 'build-local.ps1'), @'
param([switch]$LauncherOnly)
if (-not $LauncherOnly) { throw 'Docker build compiled unwanted host services.' }
$global:DanmakuFlowDockerTest.Calls += ,@('host-build')
'@)
    $composeFile = Join-Path $fixture 'docker-compose.yaml'
    [IO.File]::WriteAllText($composeFile, @'
services:
  fixture:
    image: busybox:latest
    environment:
      MYSQL_ROOT_PASSWORD: ${MYSQL_ROOT_PASSWORD:?required}
      DANMAKUFLOW_MYSQL_DSN: ${DANMAKUFLOW_MYSQL_DSN:?required}
'@)
    $setup = Join-Path $scripts 'setup-docker.ps1'
    $local = Join-Path $fixture '.local'
    $dockerDir = Join-Path $local 'docker'
    $configPath = Join-Path $local 'launcher.json'
    $secretPath = Join-Path $local 'mysql-dsn.xml'
    $envPath = Join-Path $dockerDir 'docker.env'

    & $setup -DockerExecutable $docker -MySQLRootPassword $secure
    foreach ($path in @($local, $dockerDir, $configPath, $secretPath, $envPath)) { Assert-PrivateAccess $path }
    $config = [IO.File]::ReadAllText($configPath) | ConvertFrom-Json
    if ($config.version -ne 1 -or $config.backend -ne 'docker' -or $config.repoDir -ne $fixture -or
        $config.binDir -ne (Join-Path $fixture 'bin') -or $config.docker.executable -ne $docker -or
        $config.docker.context -ne 'desktop-linux' -or $config.docker.project -ne 'danmakuflow-local' -or
        $config.docker.composeFile -ne $composeFile -or $config.docker.envFile -ne $envPath) {
        throw 'Docker launcher configuration does not match the requested workspace.'
    }
    $hostDSN = 'root:' + $password + '@tcp(127.0.0.1:3306)/danmakuflow?charset=utf8mb4&parseTime=True&loc=Local'
    $containerDSN = 'root:' + $password + '@tcp(mysql:3306)/danmakuflow?charset=utf8mb4&parseTime=True&loc=Local'
    if ((Read-SavedDSN $secretPath) -cne $hostDSN) { throw 'Saved DSN lost the password or host target.' }
    if ([IO.File]::ReadAllText($secretPath).Contains($password)) { throw 'Saved DSN is plaintext.' }
    $envText = [IO.File]::ReadAllText($envPath)
    $expectedPasswordLine = 'MYSQL_ROOT_PASSWORD="test$$money # ''single'' \"double\" \\tail\\"'
    if (($envText -split "`r?`n") -cnotcontains $expectedPasswordLine) { throw 'Dotenv password escaping changed its value.' }
    if ($envText.Contains('127.0.0.1:3306') -or -not $envText.Contains('@tcp(mysql:3306)/danmakuflow?')) {
        throw 'Container DSN points at the wrong host.'
    }
    if ($ComposeExecutable) {
        try {
            Remove-Item -LiteralPath Env:DANMAKUFLOW_MYSQL_DSN, Env:MYSQL_ROOT_PASSWORD -ErrorAction SilentlyContinue
            $parsed = & $ComposeExecutable --context desktop-linux compose --env-file $envPath --file $composeFile --project-name danmakuflow-fixture config --format json 2>&1
            if ($LASTEXITCODE -ne 0) { throw ('Compose could not parse fixture credentials: ' + ($parsed -join "`n")) }
        } finally {
            Restore-TestEnvironment
        }
        $actual = ($parsed | ConvertFrom-Json).services.fixture.environment
        # Compose config escapes each resolved $ to $$ so its output can be
        # reused as Compose input; decode that rendering before comparing.
        if ($actual.MYSQL_ROOT_PASSWORD.Replace('$$', '$') -cne $password -or
            $actual.DANMAKUFLOW_MYSQL_DSN.Replace('$$', '$') -cne $containerDSN) {
            throw ('Compose did not preserve fixture credentials: ' + ($actual | ConvertTo-Json -Compress))
        }
    }

    $beforeConfig = [IO.File]::ReadAllText($configPath)
    $beforeSecret = [IO.File]::ReadAllText($secretPath)
    foreach ($invalidOption in @(@{ DockerContext = 'remote-context' }, @{ ProjectName = 'other-project' })) {
        $rejected = $false
        try { & $setup -DockerExecutable $docker -MySQLRootPassword $secure @invalidOption } catch { $rejected = $true }
        if (-not $rejected -or [IO.File]::ReadAllText($configPath) -cne $beforeConfig) {
            throw 'Setup accepted a Docker context/project unsupported by the runtime.'
        }
    }
    & $setup -DockerExecutable $docker -MySQLRootPassword $secure
    $backup = @(Get-ChildItem -LiteralPath (Join-Path $dockerDir 'backups') -Directory)
    if ($backup.Count -ne 1 -or [IO.File]::ReadAllText((Join-Path $backup[0].FullName 'launcher.json')) -cne $beforeConfig -or
        [IO.File]::ReadAllText((Join-Path $backup[0].FullName 'mysql-dsn.xml')) -cne $beforeSecret -or
        [IO.File]::ReadAllText((Join-Path $backup[0].FullName 'docker.env')) -cne $envText) {
        throw 'Repeated setup did not preserve the complete previous configuration.'
    }
    Assert-PrivateAccess $backup[0].FullName
    foreach ($file in Get-ChildItem -LiteralPath $backup[0].FullName -File) { Assert-PrivateAccess $file.FullName }
    $beforeSecret = [IO.File]::ReadAllText($secretPath)
    $busyLauncher = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 4174)
    $busyLauncher.Start()
    try {
        $rejected = $false
        try { & $setup -DockerExecutable $docker -MySQLRootPassword $secure } catch { $rejected = $true }
        if (-not $rejected -or [IO.File]::ReadAllText($configPath) -cne $beforeConfig) {
            throw 'Setup replaced configuration while a launcher still owned port 4174.'
        }
    } finally { $busyLauncher.Stop() }

    $runtime = Join-Path $local 'runtime'
    New-Item -ItemType Directory -Path $runtime -Force | Out-Null
    $state = Join-Path $runtime 'instances.json'
    foreach ($invalidState in @('{"instances":{"mysql":{"owned":true}}}', 'invalid JSON')) {
        [IO.File]::WriteAllText($state, $invalidState)
        $rejected = $false
        try { & $setup -DockerExecutable $docker -MySQLRootPassword $secure } catch { $rejected = $true }
        if (-not $rejected -or [IO.File]::ReadAllText($configPath) -cne $beforeConfig -or
            [IO.File]::ReadAllText($secretPath) -cne $beforeSecret -or [IO.File]::ReadAllText($envPath) -cne $envText) {
            throw 'Setup changed credentials despite an active or unreadable runtime record.'
        }
    }
    [IO.File]::WriteAllText($state, '{"instances":{"mysql":{"owned":true,"stopped":true}}}')
    foreach ($invalidPassword in @((New-Object Security.SecureString), (ConvertTo-SecureString "bad`npassword" -AsPlainText -Force))) {
        $rejected = $false
        try { & $setup -DockerExecutable $docker -MySQLRootPassword $invalidPassword } catch { $rejected = $true }
        if (-not $rejected -or [IO.File]::ReadAllText($secretPath) -cne $beforeSecret) { throw 'Invalid password overwrote credentials.' }
    }

    $global:DanmakuFlowDockerTest.Calls = @()
    $env:DANMAKUFLOW_MYSQL_DSN = 'fixture-host-dsn'
    $env:MYSQL_ROOT_PASSWORD = 'fixture-host-password'
    & (Join-Path $scripts 'build-docker.ps1')
    if ($env:DANMAKUFLOW_MYSQL_DSN -ne 'fixture-host-dsn' -or $env:MYSQL_ROOT_PASSWORD -ne 'fixture-host-password') {
        throw 'Build did not restore the caller environment.'
    }
    $calls = $global:DanmakuFlowDockerTest.Calls
    if ($calls.Count -ne 5 -or $calls[0][0] -ne 'host-build') { throw 'Docker build did not build the host launcher first.' }
    $baseArgs = @('--context', 'desktop-linux', 'compose', '--env-file', $envPath, '--file', $composeFile, '--project-name', 'danmakuflow-local', '--parallel', '1')
    $actions = @(@('build', 'server'), @('pull', 'mysql'), @('pull', 'redis'), @('pull', 'kafka'))
    for ($i = 0; $i -lt $actions.Count; $i++) {
        if (($calls[$i + 1] | ConvertTo-Json -Compress) -cne (($baseArgs + $actions[$i]) | ConvertTo-Json -Compress)) {
            throw 'Docker build changed the explicit context/project or serial command order.'
        }
    }

    New-Item -ItemType Directory -Path (Join-Path $fixture 'bin'), (Join-Path $fixture 'web\dist') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $fixture 'bin\launcher.exe'), 'fixture')
    [IO.File]::WriteAllText((Join-Path $fixture 'web\dist\index.html'), 'fixture')
    & (Join-Path $scripts 'start-local.ps1')
    if (-not $global:DanmakuFlowDockerTest.BrowserOpened) { throw 'Docker launcher required host service binaries.' }
    $global:DanmakuFlowDockerTest.LauncherReady = $false
    & (Join-Path $scripts 'start-local.ps1')
    if ($global:DanmakuFlowDockerTest.LauncherDSN -cne $hostDSN -or $env:DANMAKUFLOW_MYSQL_DSN -ne 'fixture-host-dsn') {
        throw 'Docker launcher inherited an unrelated host DSN or did not restore it.'
    }
    Remove-Item -LiteralPath $secretPath
    $rejected = $false
    try { & (Join-Path $scripts 'start-local.ps1') } catch { $rejected = $true }
    if (-not $rejected) { throw 'Docker launcher accepted inherited credentials without its saved DSN.' }
    Write-Output 'PASS: private Docker setup, backups, active-state protection, serial build and Docker launch artifact selection.'
} finally {
    Restore-TestEnvironment
    $target = [IO.Path]::GetFullPath($fixture)
    if (-not $target.StartsWith($testBase, [StringComparison]::OrdinalIgnoreCase) -or
        (Split-Path -Leaf $target) -notmatch '^danmakuflow-docker-test-[0-9a-f]{32}$') {
        throw 'Refusing cleanup outside the temporary test fixture.'
    }
    if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
    Remove-Variable -Name DanmakuFlowDockerTest -Scope Global -ErrorAction SilentlyContinue
}
