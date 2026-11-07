param(
    [Security.SecureString]$MySQLRootPassword,
    [string]$DockerExecutable,
    [ValidateSet('desktop-linux')]
    [string]$DockerContext = 'desktop-linux',
    [ValidateSet('danmakuflow-local')]
    [string]$ProjectName = 'danmakuflow-local'
)

$ErrorActionPreference = 'Stop'
$configurationListener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 4174)
try { $configurationListener.Start() }
catch { throw 'Close the existing local launcher on port 4174 before changing Docker configuration.' }
try {
$repo = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$local = Join-Path $repo '.local'
$dockerDir = Join-Path $local 'docker'
$configPath = Join-Path $local 'launcher.json'
$secretPath = Join-Path $local 'mysql-dsn.xml'
$envPath = Join-Path $dockerDir 'docker.env'
$composeFile = Join-Path $repo 'docker-compose.yaml'
$statePath = Join-Path $local 'runtime\instances.json'

if (Test-Path -LiteralPath $statePath) {
    try { $saved = [IO.File]::ReadAllText($statePath) | ConvertFrom-Json }
    catch { throw 'Cannot read existing runtime state. Restore it before changing backend configuration.' }
    if ($null -eq $saved -or $null -eq $saved.instances) { throw 'Runtime state is invalid; backend configuration was preserved.' }
    foreach ($instance in $saved.instances.PSObject.Properties.Value) {
        if ($instance.owned -eq $true -and $instance.stopped -ne $true) {
            throw 'Stop all owned services with the existing launcher before changing backend configuration.'
        }
    }
}
if ([string]::IsNullOrWhiteSpace($DockerContext)) { throw 'Docker context cannot be empty.' }
if (-not (Test-Path -LiteralPath $composeFile -PathType Leaf)) { throw 'docker-compose.yaml is missing from this repository.' }
if ([string]::IsNullOrWhiteSpace($DockerExecutable)) {
    $candidates = @()
    if ($env:LOCALAPPDATA) { $candidates += (Join-Path $env:LOCALAPPDATA 'Programs\DockerDesktop\resources\bin\docker.exe') }
    if ($env:ProgramFiles) { $candidates += (Join-Path $env:ProgramFiles 'Docker\Docker\resources\bin\docker.exe') }
    $command = Get-Command docker.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($command) { $candidates += $command.Source }
    $DockerExecutable = $candidates | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
}
if ([string]::IsNullOrWhiteSpace($DockerExecutable) -or -not [IO.Path]::IsPathRooted($DockerExecutable) -or
    -not (Test-Path -LiteralPath $DockerExecutable -PathType Leaf)) {
    throw 'Docker CLI was not found. Install Docker Desktop or pass an existing absolute -DockerExecutable path.'
}
$DockerExecutable = [IO.Path]::GetFullPath($DockerExecutable)
try {
    & $DockerExecutable --context $DockerContext compose version 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Compose unavailable.' }
} catch { throw 'Docker Compose is unavailable. Install the Docker Desktop Compose plugin.' }

function Save-PrivateAccessRules([string]$Path, [bool]$Directory = $false) {
    # Persist clears the security object's modified flags. Every target needs
    # a new object so repeated setup and all subsequent files write their ACL.
    $rules = if ($Directory) { New-Object Security.AccessControl.DirectorySecurity } else { New-Object Security.AccessControl.FileSecurity }
    $rules.SetAccessRuleProtection($true, $false)
    foreach ($principal in @($sid, $system)) {
        $rule = if ($Directory) {
            New-Object Security.AccessControl.FileSystemAccessRule($principal, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        } else {
            New-Object Security.AccessControl.FileSystemAccessRule($principal, 'FullControl', 'Allow')
        }
        $rules.AddAccessRule($rule)
    }
    # Persist only access rules, avoiding SeSecurityPrivilege for audit rules.
    $item = Get-Item -LiteralPath $Path
    if ($PSVersionTable.PSEdition -eq 'Core') {
        [IO.FileSystemAclExtensions]::SetAccessControl($item, $rules)
    } else {
        $item.SetAccessControl($rules)
    }
}
function New-PrivateDirectory([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { New-Item -ItemType Directory -Path $Path | Out-Null }
    Save-PrivateAccessRules $Path $true
}
function ConvertTo-DotEnvValue([string]$Value) {
    # Compose double quotes decode backslashes; $$ prevents interpolation.
    return '"' + $Value.Replace('\', '\\').Replace('"', '\"').Replace('$', '$$') + '"'
}
function Write-PrivateText([string]$Path, [string]$Value) {
    if (Test-Path -LiteralPath $Path) { Save-PrivateAccessRules $Path }
    [IO.File]::WriteAllText($Path, $Value, (New-Object Text.UTF8Encoding($false)))
    Save-PrivateAccessRules $Path
}

if (-not $PSBoundParameters.ContainsKey('MySQLRootPassword')) {
    $MySQLRootPassword = Read-Host 'Enter the Docker MySQL root password (input hidden)' -AsSecureString
}
if ($null -eq $MySQLRootPassword -or $MySQLRootPassword.Length -eq 0) { throw 'MySQL password cannot be empty.' }
$ptr = [IntPtr]::Zero
try {
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($MySQLRootPassword)
    $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
    if ($plain -match '[\x00-\x1f\x7f]') { throw 'MySQL password cannot contain control characters or line breaks.' }
    $hostDSN = 'root:' + $plain + '@tcp(127.0.0.1:3306)/danmakuflow?charset=utf8mb4&parseTime=True&loc=Local'
    $containerDSN = 'root:' + $plain + '@tcp(mysql:3306)/danmakuflow?charset=utf8mb4&parseTime=True&loc=Local'
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $system = New-Object Security.Principal.SecurityIdentifier('S-1-5-18')
    New-PrivateDirectory $local
    New-PrivateDirectory $dockerDir

    $existing = @($configPath, $secretPath, $envPath) | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf }
    if ($existing) {
        $backups = Join-Path $dockerDir 'backups'
        New-PrivateDirectory $backups
        $backup = Join-Path $backups ((Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N'))
        New-PrivateDirectory $backup
        foreach ($path in $existing) {
            $destination = Join-Path $backup (Split-Path -Leaf $path)
            Copy-Item -LiteralPath $path -Destination $destination
            Save-PrivateAccessRules $destination
        }
    }

    $config = [ordered]@{
        version = 1
        backend = 'docker'
        repoDir = $repo
        binDir = (Join-Path $repo 'bin')
        docker = [ordered]@{
            executable = $DockerExecutable
            context = $DockerContext
            project = $ProjectName
            composeFile = $composeFile
            envFile = $envPath
        }
    }
    Write-PrivateText $envPath (('MYSQL_ROOT_PASSWORD=' + (ConvertTo-DotEnvValue $plain)) + "`n" +
        ('DANMAKUFLOW_MYSQL_DSN=' + (ConvertTo-DotEnvValue $containerDSN)) + "`n")
    if (Test-Path -LiteralPath $secretPath) { Save-PrivateAccessRules $secretPath }
    ConvertTo-SecureString $hostDSN -AsPlainText -Force | Export-Clixml -LiteralPath $secretPath
    Save-PrivateAccessRules $secretPath
    Write-PrivateText $configPath ($config | ConvertTo-Json -Depth 5)
} finally {
    if ($ptr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
    $plain = $null
    $hostDSN = $null
    $containerDSN = $null
}
Write-Host 'Docker configuration saved locally. No services were started or schema migrations performed.'
} finally { $configurationListener.Stop() }
