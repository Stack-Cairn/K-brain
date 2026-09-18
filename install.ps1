param(
    [string]$Version = 'latest',
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\k-brain')
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$repo = 'Stack-Cairn/K-brain'
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
    if (($userPath -split ';') -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $InstallDir).TrimStart(';'), 'User')
    }
    Write-Output "Installed $target. Open a new terminal and run kn."
} finally {
    if (Test-Path -LiteralPath $download) { Remove-Item -LiteralPath $download -Force }
    if (Test-Path -LiteralPath $helperDownload) { Remove-Item -LiteralPath $helperDownload -Force }

    if (Test-Path -LiteralPath $backup) { Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue }
}
