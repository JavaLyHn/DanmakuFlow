$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$configPath = Join-Path $repo '.local\launcher.json'
if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) { throw 'Run scripts\setup-docker.ps1 first.' }
$config = [IO.File]::ReadAllText($configPath) | ConvertFrom-Json
if ($config.version -ne 1 -or $config.backend -ne 'docker' -or $null -eq $config.docker) {
    throw 'Docker backend configuration is required. Run scripts\setup-docker.ps1 explicitly.'
}
foreach ($path in @($config.docker.executable, $config.docker.composeFile, $config.docker.envFile)) {
    if ([string]::IsNullOrWhiteSpace($path) -or -not [IO.Path]::IsPathRooted($path) -or
        -not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'A configured Docker file is missing or is not an absolute path.' }
}
if ([string]::IsNullOrWhiteSpace($config.docker.context) -or [string]::IsNullOrWhiteSpace($config.docker.project)) {
    throw 'Docker context and project must be configured explicitly.'
}
& (Join-Path $PSScriptRoot 'build-local.ps1') -LauncherOnly
$composeArgs = @('--context', $config.docker.context, 'compose', '--env-file', $config.docker.envFile,
    '--file', $config.docker.composeFile, '--project-name', $config.docker.project, '--parallel', '1')
$savedEnvironment = @{}
try {
    # Prevent host DSNs and Compose overrides from replacing the explicit files.
    foreach ($entry in @(Get-ChildItem Env:)) {
        if ($entry.Name -like 'DANMAKUFLOW_*' -or $entry.Name -like 'COMPOSE_*' -or $entry.Name -eq 'MYSQL_ROOT_PASSWORD') {
            $savedEnvironment[$entry.Name] = $entry.Value
            Remove-Item -LiteralPath ('Env:' + $entry.Name)
        }
    }
    $env:COMPOSE_PARALLEL_LIMIT = '1'
    & $config.docker.executable @composeArgs build server
    if ($LASTEXITCODE -ne 0) { throw 'Docker application image build failed.' }
    foreach ($service in @('mysql', 'redis', 'kafka')) {
        & $config.docker.executable @composeArgs pull $service
        if ($LASTEXITCODE -ne 0) { throw "Docker image pull failed: $service" }
    }
} finally {
    Remove-Item -LiteralPath Env:COMPOSE_PARALLEL_LIMIT -ErrorAction SilentlyContinue
    foreach ($entry in $savedEnvironment.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
}
Write-Host 'Docker images and host launcher built. No services were started.'
