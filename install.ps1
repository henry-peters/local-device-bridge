param(
  [switch]$FromSource,
  [switch]$NoSetup,
  [string]$Repository = $(if ($env:LDB_REPOSITORY) { $env:LDB_REPOSITORY } else { 'local-device-bridge/local-device-bridge' }),
  [string]$Version = $(if ($env:LDB_VERSION) { $env:LDB_VERSION } else { 'latest' }),
  [string]$InstallDir = $(if ($env:LDB_INSTALL_DIR) { $env:LDB_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'local-device-bridge' })
)

$ErrorActionPreference = 'Stop'

function Write-Step([string]$Message) { Write-Host "  -> $Message" -ForegroundColor Cyan }
function Write-Ok([string]$Message) { Write-Host "  [OK] $Message" -ForegroundColor Green }
function Stop-Install([string]$Message) { Write-Host "  [ERROR] $Message" -ForegroundColor Red; exit 1 }

Write-Host ''
Write-Host '------------------------------------------------------------------------' -ForegroundColor DarkCyan
Write-Host '  local-device-bridge  //  INSTALLER' -ForegroundColor White
Write-Host '  Discover  |  Pair  |  Control  |  Audit' -ForegroundColor Yellow
Write-Host '------------------------------------------------------------------------' -ForegroundColor DarkCyan
Write-Host ''

$arch = $env:PROCESSOR_ARCHITECTURE.ToLowerInvariant()
if ($arch -notin @('amd64', 'arm64')) { Stop-Install "Unsupported CPU architecture: $arch" }
if ($arch -eq 'arm64') { Stop-Install 'Windows ARM64 release artifacts are not published yet. Build from source with Go on this machine.' }
Write-Ok "Platform detected: windows/$arch"

$temp = Join-Path ([System.IO.Path]::GetTempPath()) ("local-device-bridge-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temp | Out-Null
try {
  $binary = Join-Path $temp 'local-device-bridge.exe'
  if ($FromSource) {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Stop-Install 'Go 1.26+ is required for a source install.' }
    $repoRoot = $PSScriptRoot
    if (-not (Test-Path (Join-Path $repoRoot 'go.mod'))) { Stop-Install 'Run install.ps1 from the repository root.' }
    Write-Step 'Building the self-contained CLI'
    & go build -trimpath -ldflags '-s -w' -o $binary (Join-Path $repoRoot 'cmd/local-device-bridge')
    if ($LASTEXITCODE -ne 0) { Stop-Install 'Go build failed.' }
    Write-Ok 'Binary built without external UI packages'
  } else {
    if ($Version -eq 'latest') {
      Write-Step 'Finding the latest published release'
      $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repository/releases/latest"
      $Version = $release.tag_name
    }
    $archive = "local-device-bridge_windows_amd64.zip"
    $archivePath = Join-Path $temp $archive
    $checksumPath = Join-Path $temp 'checksums.txt'
    Write-Step "Downloading $Repository $Version"
    Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/$Repository/releases/download/$Version/$archive" -OutFile $archivePath
    Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/$Repository/releases/download/$Version/checksums.txt" -OutFile $checksumPath
    $checksumLine = Get-Content $checksumPath | Where-Object { $_ -match "\s$([regex]::Escape($archive))$" } | Select-Object -First 1
    if (-not $checksumLine) { Stop-Install "The release checksum did not list $archive." }
    $expected = ($checksumLine -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    if ($expected -ne $actual) { Stop-Install 'The downloaded release checksum did not match.' }
    Write-Ok 'Release checksum verified'
    Expand-Archive -Path $archivePath -DestinationPath $temp -Force
    if (-not (Test-Path $binary)) { Stop-Install 'The release did not contain local-device-bridge.exe.' }
    Write-Ok 'Release downloaded'
  }

  Write-Step "Installing to $InstallDir"
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  $target = Join-Path $InstallDir 'local-device-bridge.exe'
  Copy-Item -Force $binary $target
  Write-Ok "Installed $target"

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if ($userPath -notlike "*${InstallDir}*") {
    [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ';' + $InstallDir).Trim(';')), 'User')
    Write-Host "  [NOTE] Open a new PowerShell window to use local-device-bridge from PATH." -ForegroundColor Yellow
  }

  if (-not $NoSetup) {
    Write-Step 'Opening the first-run setup wizard'
    & $target setup
  } else {
    Write-Host "Setup was skipped for this install. Run $target setup later if needed."
  }
  Write-Host ''
  Write-Host 'Installation complete.' -ForegroundColor Green
  if (-not $NoSetup) {
    Write-Host 'The setup wizard handled the selected startup mode.'
  } else {
    Write-Host 'No daemon was started because setup was skipped.'
  }
} finally {
  Remove-Item -Recurse -Force -LiteralPath $temp -ErrorAction SilentlyContinue
}
