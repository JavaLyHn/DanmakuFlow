$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$local = Join-Path $repo '.local'
if (-not (Test-Path -LiteralPath $local)) { New-Item -ItemType Directory -Path $local | Out-Null }
# Do not inherit broad permissions for credentials or runtime state.
$acl = New-Object System.Security.AccessControl.DirectorySecurity
$acl.SetAccessRuleProtection($true, $false)
$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$system = New-Object System.Security.Principal.SecurityIdentifier('S-1-5-18')
$inherit = [System.Security.AccessControl.InheritanceFlags]'ContainerInherit,ObjectInherit'
foreach ($principal in @($sid, $system)) {
    $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($principal, 'FullControl', $inherit, 'None', 'Allow')
    $acl.AddAccessRule($rule)
}
Set-Acl -LiteralPath $local -AclObject $acl
function Choose-Path([string]$label, [string]$candidate, [bool]$directory) {
    $default = if (Test-Path -LiteralPath $candidate -PathType $(if ($directory) {'Container'} else {'Leaf'})) { $candidate } else { '' }
    do {
        $answer = Read-Host "$label [$default]"
        if ([string]::IsNullOrWhiteSpace($answer)) { $answer = $default }
        $valid = [System.IO.Path]::IsPathRooted($answer) -and (Test-Path -LiteralPath $answer -PathType $(if ($directory) {'Container'} else {'Leaf'}))
        if (-not $valid) { Write-Host 'Enter an existing absolute path.' }
    } until ($valid)
    return [System.IO.Path]::GetFullPath($answer)
}
$baseline = 'D:\LiveStream\baseline-20261106'
$redis = Join-Path $baseline 'tools\redis-7.4.11\Redis-7.4.11-Windows-x64-msys2'
$configPath = Join-Path $local 'launcher.json'
if ((Test-Path -LiteralPath $configPath) -and ((Read-Host 'Replace existing launcher configuration? [y/N]') -notmatch '^[yY]$')) {
    Write-Host 'Existing configuration preserved.'
} else {
    $config = [ordered]@{
        version = 1; repoDir = $repo; binDir = (Join-Path $repo 'bin')
        mysql = [ordered]@{ executable = (Choose-Path 'MySQL executable' 'D:\DevTools\mysql-8.4.13-winx64\bin\mysqld.exe' $false); configFile = (Choose-Path 'MySQL config' 'D:\DevTools\mysql.ini' $false) }
        redis = [ordered]@{ executable = (Choose-Path 'Redis executable' (Join-Path $redis 'redis-server.exe') $false); configFile = (Choose-Path 'Redis config' (Join-Path $baseline 'config\redis.conf') $false); workingDir = (Choose-Path 'Redis working directory' $redis $true) }
        kafka = [ordered]@{ javaExecutable = (Choose-Path 'Java executable' 'D:\DevTools\jdk-21.0.13+9\bin\java.exe' $false); home = (Choose-Path 'Kafka home' (Join-Path $baseline 'tools\kafka_2.13-4.1.2') $true); configFile = (Choose-Path 'Kafka config' (Join-Path $baseline 'config\kafka.properties') $false) }
    }
    [System.IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 5), (New-Object System.Text.UTF8Encoding($false)))
}
$secretPath = Join-Path $local 'mysql-dsn.xml'
if ((Test-Path -LiteralPath $secretPath) -and ((Read-Host 'Replace existing saved DSN? [y/N]') -notmatch '^[yY]$')) {
    Write-Host 'Existing DSN preserved.'
} else {
    $secret = Read-Host 'Enter complete local MySQL DSN (input hidden)' -AsSecureString
    if ($secret.Length -eq 0) { throw 'DSN cannot be empty.' }
    $secret | Export-Clixml -LiteralPath $secretPath
}
Write-Host 'Local configuration saved. No services were started.'

# Apply an explicit ACL even if an existing secret had custom permissions.
$fileAcl = New-Object System.Security.AccessControl.FileSecurity
$fileAcl.SetAccessRuleProtection($true, $false)
foreach ($principal in @($sid, $system)) {
    $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($principal, 'FullControl', 'Allow')
    $fileAcl.AddAccessRule($rule)
}
Set-Acl -LiteralPath $secretPath -AclObject $fileAcl
