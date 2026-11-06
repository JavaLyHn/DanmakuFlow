# Runs the real setup script against temporary files and a dummy secret only.
$ErrorActionPreference = 'Stop'
$source = Join-Path (Split-Path -Parent $PSScriptRoot) 'setup-local.ps1'
$testBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$script:fixtureRoot = Join-Path $testBase ('danmakuflow-setup-test-' + [Guid]::NewGuid().ToString('N'))
$fixtureScripts = Join-Path $script:fixtureRoot 'scripts'
$script:dummyFile = Join-Path $script:fixtureRoot 'component.stub'
$script:dummySecret = 'test-only-not-a-database-credential'
$global:DanmakuFlowSetupTestAnswers = @{ Root = $script:fixtureRoot; File = $script:dummyFile; Secret = $script:dummySecret; KeepExisting = $false }

function Read-Host {
    param([string]$Prompt, [switch]$AsSecureString)
    if ($AsSecureString) { return ConvertTo-SecureString $global:DanmakuFlowSetupTestAnswers.Secret -AsPlainText -Force }
    if ($Prompt -like 'Replace existing*') { if ($global:DanmakuFlowSetupTestAnswers.KeepExisting) { return 'n' }; return 'y' }
    if ($Prompt -like 'Redis working directory*' -or $Prompt -like 'Kafka home*') { return $global:DanmakuFlowSetupTestAnswers.Root }
    return $global:DanmakuFlowSetupTestAnswers.File
}

function Assert-PrivateAccess([string]$Path) {
    $acl = Get-Acl -LiteralPath $Path
    if (-not $acl.AreAccessRulesProtected) { throw "Access inheritance remained enabled: $Path" }
    $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    $expected = @([Security.Principal.WindowsIdentity]::GetCurrent().User.Value, 'S-1-5-18')
    if ($rules.Count -ne 2) { throw "Unexpected access rules: $Path" }
    foreach ($rule in $rules) {
        if ($rule.IsInherited -or $rule.IdentityReference.Value -notin $expected -or
            $rule.AccessControlType -ne 'Allow' -or $rule.FileSystemRights -ne 'FullControl') {
            throw "Unexpected access grant: $Path"
        }
    }
    foreach ($sid in $expected) {
        if (@($rules | Where-Object { $_.IdentityReference.Value -eq $sid }).Count -ne 1) { throw 'Expected principal missing' }
    }
}

try {
    New-Item -ItemType Directory -Path $fixtureScripts -Force | Out-Null
    [IO.File]::WriteAllText($script:dummyFile, 'not an executable')
    Copy-Item -LiteralPath $source -Destination (Join-Path $fixtureScripts 'setup-local.ps1')
    $setup = Join-Path $fixtureScripts 'setup-local.ps1'
    $local = Join-Path $script:fixtureRoot '.local'
    $secret = Join-Path $local 'mysql-dsn.xml'
    $config = Join-Path $local 'launcher.json'

    & $setup
    Assert-PrivateAccess $local
    Assert-PrivateAccess $secret
    $parsed = [IO.File]::ReadAllText($config) | ConvertFrom-Json
    if ($parsed.version -ne 1 -or $parsed.repoDir -ne $script:fixtureRoot) { throw 'Setup did not write its configuration' }
    if ((Import-Clixml -LiteralPath $secret) -isnot [Security.SecureString]) { throw 'Secret was not stored as a secure string' }
    if ([IO.File]::ReadAllText($secret).Contains($script:dummySecret)) { throw 'Secret was written in plaintext' }
    $beforeConfig = [IO.File]::ReadAllText($config)
    $beforeSecret = [IO.File]::ReadAllText($secret)

    # Re-running setup on an already protected directory must work without
    # changing either the saved configuration or credentials when declined.
    $global:DanmakuFlowSetupTestAnswers.KeepExisting = $true
    & $setup
    Assert-PrivateAccess $local
    Assert-PrivateAccess $secret
    if ([IO.File]::ReadAllText($config) -ne $beforeConfig -or [IO.File]::ReadAllText($secret) -ne $beforeSecret) {
        throw 'Setup replaced existing values despite declining replacement'
    }
    Write-Output 'PASS: first setup and repeated setup preserve private access and encrypted credentials.'
} finally {
    $target = [IO.Path]::GetFullPath($script:fixtureRoot)
    if (-not $target.StartsWith($testBase, [StringComparison]::OrdinalIgnoreCase) -or
        (Split-Path -Leaf $target) -notmatch '^danmakuflow-setup-test-[0-9a-f]{32}$') {
        throw 'Refusing cleanup outside the temporary test fixture'
    }
    if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
}
