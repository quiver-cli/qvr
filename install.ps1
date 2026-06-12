# qvr installer for Windows (PowerShell).
#
#   irm https://github.com/astra-sh/qvr/raw/main/install.ps1 | iex
#
# Downloads the prebuilt release binary (UI embedded) for your architecture from
# GitHub Releases, verifies its checksum, installs it under
# %LOCALAPPDATA%\Programs\qvr, and adds that dir to your user PATH.
#
# Env overrides:
#   $env:QVR_VERSION       pin a version, e.g. v0.12.0 (default: latest release)
#   $env:QVR_INSTALL_DIR   install location (default: %LOCALAPPDATA%\Programs\qvr)
$ErrorActionPreference = 'Stop'

$Repo = 'astra-sh/qvr'
$Binary = 'qvr'

function Info($m) { Write-Host "==> $m" -ForegroundColor Blue }
function Fail($m) { Write-Host "error: $m" -ForegroundColor Red; exit 1 }

# --- detect architecture --------------------------------------------------
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'x86_64' }
  'ARM64' { 'arm64' }
  default { Fail "unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)'" }
}

# --- record any prior install (for staleness visibility) ------------------
$priorCmd = Get-Command $Binary -ErrorAction SilentlyContinue
$priorBin = if ($priorCmd) { $priorCmd.Source } else { $null }
$priorVer = if ($priorBin) { (& $priorBin version 2>$null | Select-Object -First 1) -split '\s+' | Select-Object -Skip 1 -First 1 } else { $null }

# --- resolve version ------------------------------------------------------
$version = $env:QVR_VERSION
if (-not $version) {
  Info 'Resolving latest release...'
  $rel = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
  $version = $rel.tag_name
  if (-not $version) { Fail 'could not determine latest version (set $env:QVR_VERSION)' }
}

if ($priorVer) {
  if ($priorVer -eq $version) { Info "Already on $version ($priorBin) - reinstalling cleanly" }
  else { Info "Upgrading $priorVer -> $version (current: $priorBin)" }
}

$asset = "${Binary}_Windows_${arch}.zip"
$base = "https://github.com/$Repo/releases/download/$version"
Info "Installing $Binary $version (Windows/$arch)"

# --- download + verify ----------------------------------------------------
$tmp = Join-Path $env:TEMP ("qvr-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  $zip = Join-Path $tmp $asset
  Invoke-WebRequest "$base/$asset" -OutFile $zip

  $sums = Join-Path $tmp 'checksums.txt'
  Invoke-WebRequest "$base/checksums.txt" -OutFile $sums
  $want = (Get-Content $sums | Where-Object { $_ -match [regex]::Escape($asset) }) -split '\s+' | Select-Object -First 1
  if (-not $want) { Fail "checksum not found in checksums.txt for $asset" }
  $got = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
  if ($got -ne $want.ToLower()) { Fail "checksum mismatch for $asset" }
  Info 'Checksum verified'

  Expand-Archive -Path $zip -DestinationPath $tmp -Force
  $exe = Join-Path $tmp "$Binary.exe"
  if (-not (Test-Path $exe)) { Fail "archive did not contain $Binary.exe" }

  # --- install ------------------------------------------------------------
  $dir = $env:QVR_INSTALL_DIR
  if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA "Programs\$Binary" }
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Copy-Item $exe (Join-Path $dir "$Binary.exe") -Force
  Info "Installed to $dir\$Binary.exe"

  # --- PATH ---------------------------------------------------------------
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $pathDirs = $userPath -split ';' | Where-Object { $_ }
  if ($pathDirs -notcontains $dir) {
    $newPath = if ($userPath) { "$userPath;$dir" } else { $dir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Info "Added $dir to your user PATH — restart your terminal to use 'qvr'."
  }
  # Zero-staleness check: confirm PATH resolves to the copy we just wrote, not
  # an older one earlier in PATH that would silently shadow this upgrade.
  $resolved = (Get-Command $Binary -ErrorAction SilentlyContinue).Source
  $installed = Join-Path $dir "$Binary.exe"
  if ($resolved -and ($resolved -ne $installed)) {
    Write-Host "warning: a different $Binary is earlier on PATH and will shadow this install:" -ForegroundColor Yellow
    Write-Host "  shadowing : $resolved"
    Write-Host "  installed : $installed ($version)"
    Write-Host "Remove the stale copy or put $dir earlier on PATH."
  }
  & $(if ($resolved) { $resolved } else { $installed }) version
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
