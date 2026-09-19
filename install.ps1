param(
    [string]$Version = 'latest',
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\k-brain')
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$repo = 'Stack-Cairn/K-brain'

function Get-PrioritizedPath([string]$PathValue, [string]$Directory) {
    $key = $Directory.TrimEnd('\', '/')
    $remaining = foreach ($entry in ($PathValue -split ';')) {
        $normalized = [Environment]::ExpandEnvironmentVariables($entry.Trim().Trim('"')).TrimEnd('\', '/')
        if ($normalized -and -not [string]::Equals($normalized, $key, [StringComparison]::OrdinalIgnoreCase)) {
            $entry
        }
    }
    return (@($Directory) + @($remaining)) -join ';'
}

$arch = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $arch = $env:PROCESSOR_ARCHITEW6432 }
switch ($arch) {
    'AMD64' { $asset = 'k-brain-windows-x64.exe'; $helperAsset = 'k-brain-computer-windows-x64.exe' }
    'ARM64' { $asset = 'k-brain-windows-arm64.exe'; $helperAsset = 'k-brain-computer-windows-arm64.exe' }
    default { throw "Unsupported Windows architecture: $arch" }
}
if ($Version -eq 'latest') {
    $base = "https://github.com/$repo/releases/latest/download"
} else {
    if ($Version -notmatch '^v[0-9][A-Za-z0-9._-]*$') { throw 'Invalid release version' }
    $base = "https://github.com/$repo/releases/download/$Version"
}

$InstallDir = [IO.Path]::GetFullPath($InstallDir)
New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
$download = Join-Path $InstallDir ('.k-brain-' + [Guid]::NewGuid().ToString('N') + '.exe')
$helperDownload = Join-Path $InstallDir ('.k-brain-computer-' + [Guid]::NewGuid().ToString('N') + '.exe')
$target = Join-Path $InstallDir 'kn.exe'
$helperTarget = Join-Path $InstallDir 'k-brain-computer.exe'
$backup = Join-Path $InstallDir ('.k-brain-old-' + [Guid]::NewGuid().ToString('N') + '.exe')
try {
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $download
    $sums = (Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS").Content
    if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
    $expected = $null
    foreach ($line in ($sums -split "`n")) {
        if ($line.Trim() -match ('^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($asset) + '$')) {
            $expected = $Matches[1]
        }
    }
    if (-not $expected) { throw "No checksum found for $asset" }
    if ((Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash -ne $expected) {
        throw 'Downloaded binary checksum mismatch'
    }
    $installedVersion = [string](& $download --version)
    if ($LASTEXITCODE -ne 0 -or $installedVersion -notmatch '^k-brain (v[0-9][A-Za-z0-9._-]*)$') {
        throw "Downloaded binary did not report a release version: $installedVersion"
    }
    $installedTag = $Matches[1]
    if ($Version -ne 'latest' -and $installedTag -ne $Version) {
        throw "Downloaded binary reports $installedTag; expected $Version"
    }
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$helperAsset" -OutFile $helperDownload
    $helperExpected = $null
    foreach ($line in ($sums -split "`n")) {
        if ($line.Trim() -match ('^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($helperAsset) + '$')) { $helperExpected = $Matches[1] }
    }
    if (-not $helperExpected) { throw "No checksum found for $helperAsset" }
    if ((Get-FileHash -LiteralPath $helperDownload -Algorithm SHA256).Hash -ne $helperExpected) { throw 'Downloaded helper checksum mismatch' }

    if (Test-Path -LiteralPath $target) { Move-Item -LiteralPath $target -Destination $backup }
    try { Move-Item -LiteralPath $download -Destination $target } catch {
        if (Test-Path -LiteralPath $backup) { Move-Item -LiteralPath $backup -Destination $target }
        throw
    }
    Move-Item -LiteralPath $helperDownload -Destination $helperTarget -Force
    $userPath = [string][Environment]::GetEnvironmentVariable('Path', 'User')
    $updatedPath = Get-PrioritizedPath $userPath $InstallDir
    if ($updatedPath -ne $userPath) {
        [Environment]::SetEnvironmentVariable('Path', $updatedPath, 'User')
    }
    $env:Path = Get-PrioritizedPath $env:Path $InstallDir
    Write-Output "Installed $installedVersion at $target. Run kn to start."
    $resolved = Get-Command kn -ErrorAction SilentlyContinue
    if ($resolved -and ($resolved.CommandType -ne 'Application' -or $resolved.Source -ne $target)) {
        Write-Warning "kn resolves to $($resolved.Definition), not the installed release. Run & '$target' or remove the conflicting alias/function."
    }
} finally {
    if (Test-Path -LiteralPath $download) { Remove-Item -LiteralPath $download -Force }
    if (Test-Path -LiteralPath $helperDownload) { Remove-Item -LiteralPath $helperDownload -Force }

    if (Test-Path -LiteralPath $backup) { Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue }
}
