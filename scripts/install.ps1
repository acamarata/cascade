<#
.SYNOPSIS
    Cascade installer for Windows.

.DESCRIPTION
    Downloads the latest (or a pinned) Cascade release from GitHub, verifies
    the SHA256 checksum, installs cascade.exe + cascaded.exe to
    %LOCALAPPDATA%\Cascade\bin, adds that directory to the current user's
    PATH, then runs the daemon install and init steps.

    Does NOT require Administrator rights.

.PARAMETER Version
    Release tag to install (e.g. v1.0.0). Defaults to the latest release.
    Overridden by environment variable CASCADE_VERSION if set.

.PARAMETER BinDir
    Destination directory for the binaries.
    Default: $env:LOCALAPPDATA\Cascade\bin
    Overridden by environment variable CASCADE_BIN_DIR if set.

.PARAMETER NoDaemon
    Skip 'cascade daemon install'. Switch flag or set CASCADE_NO_DAEMON=1.

.PARAMETER NoInit
    Skip 'cascade init --accept-defaults'. Switch flag or set CASCADE_NO_INIT=1.

.EXAMPLE
    # One-liner (PowerShell 5.1+)
    irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex

.EXAMPLE
    # Pin version
    $env:CASCADE_VERSION = "v1.0.0"
    irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex

.EXAMPLE
    # Skip daemon + init (CI / unattended)
    $env:CASCADE_NO_DAEMON = "1"; $env:CASCADE_NO_INIT = "1"
    irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex

.NOTES
    Asset pattern from .github/workflows/release.yml (Windows):
        cascade-{VERSION}-x86_64-pc-windows-msvc.zip
    Checksum file: SHA256SUMS (one entry per asset, sha256sum format).
    Minimum PowerShell version: 5.1 (ships with Windows 10+).
#>
[CmdletBinding()]
param(
    [string]$Version   = $env:CASCADE_VERSION,
    [string]$BinDir    = $env:CASCADE_BIN_DIR,
    [switch]$NoDaemon  = ($env:CASCADE_NO_DAEMON -ne $null -and $env:CASCADE_NO_DAEMON -ne ""),
    [switch]$NoInit    = ($env:CASCADE_NO_INIT   -ne $null -and $env:CASCADE_NO_INIT   -ne "")
)

$ErrorActionPreference = "Stop"

$Repo        = "acamarata/cascade"
$ReleasesUrl = "https://github.com/$Repo/releases"
$ApiLatest   = "https://api.github.com/repos/$Repo/releases/latest"

# ── helpers ────────────────────────────────────────────────────────────────────

function Write-Info  { param([string]$Msg) Write-Host "[cascade] $Msg" -ForegroundColor Green }
function Write-Warn  { param([string]$Msg) Write-Warning "[cascade] $Msg" }
function Write-Fatal { param([string]$Msg) Write-Error "[cascade] ERROR: $Msg" -ErrorAction Stop }

function Invoke-Download {
    param([string]$Url, [string]$Dest)
    try {
        $wc = [System.Net.WebClient]::new()
        $wc.Headers["User-Agent"] = "cascade-installer/1.0 PowerShell/$($PSVersionTable.PSVersion)"
        $wc.DownloadFile($Url, $Dest)
    } catch {
        Write-Fatal "Download failed: $Url`n  $_"
    }
}

function Get-Sha256 {
    param([string]$FilePath)
    $hash = Get-FileHash -Path $FilePath -Algorithm SHA256
    return $hash.Hash.ToLower()
}

function Test-Checksum {
    param([string]$FilePath, [string]$Expected)
    $actual = Get-Sha256 $FilePath
    if ($actual -ne $Expected.ToLower()) {
        Write-Fatal ("Checksum mismatch for {0}.`n  expected: {1}`n  got:      {2}`nAborted — do not use these binaries." -f [System.IO.Path]::GetFileName($FilePath), $Expected, $actual)
    }
    Write-Info "Checksum OK: $([System.IO.Path]::GetFileName($FilePath))"
}

# ── architecture detection ─────────────────────────────────────────────────────

function Get-Arch {
    # Windows installer only supports x86_64 (the only Windows target in release.yml).
    # ARM64 Windows is not yet in the release matrix — report clearly if encountered.
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    switch ($arch) {
        "X64"   { return "x86_64" }
        "Arm64" { Write-Fatal "ARM64 Windows is not yet in the release build matrix. Build from source: cargo install cascade-cli" }
        default { Write-Fatal "Unsupported architecture: $arch. Build from source: cargo install cascade-cli" }
    }
}

# ── version resolution ─────────────────────────────────────────────────────────

function Resolve-Version {
    param([string]$Pinned)
    if ($Pinned -ne "") {
        return $Pinned
    }
    Write-Info "Resolving latest release version..."
    try {
        $response = Invoke-RestMethod -Uri $ApiLatest -UseBasicParsing `
                        -Headers @{ "User-Agent" = "cascade-installer/1.0" }
        $tag = $response.tag_name
        if (-not $tag) { throw "tag_name empty" }
        return $tag
    } catch {
        Write-Fatal "Could not determine latest release. Set `$env:CASCADE_VERSION=v<N> and retry.`n  $_"
    }
}

# ── main ───────────────────────────────────────────────────────────────────────

function Main {
    # OS check
    if (-not $IsWindows -and $PSVersionTable.PSVersion.Major -ge 6) {
        Write-Fatal "This script is for Windows. Use scripts/install.sh on macOS/Linux."
    }

    $arch    = Get-Arch
    $tag     = Resolve-Version -Pinned $Version
    $version = $tag.TrimStart('v')
    Write-Info "Platform: windows/$arch"
    Write-Info "Version:  $tag"

    # Asset name — matches release.yml Windows packaging step:
    #   cascade-{VERSION}-x86_64-pc-windows-msvc.zip
    $assetName = "cascade-$version-x86_64-pc-windows-msvc.zip"
    $baseUrl   = "$ReleasesUrl/download/$tag"

    # Temp dir
    $tmp = [System.IO.Path]::Combine([System.IO.Path]::GetTempPath(), "cascade-install-$([System.IO.Path]::GetRandomFileName())")
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {

        # Download SHA256SUMS
        Write-Info "Downloading SHA256SUMS..."
        $checksumFile = Join-Path $tmp "SHA256SUMS"
        Invoke-Download "$baseUrl/SHA256SUMS" $checksumFile

        # Download zip
        Write-Info "Downloading $assetName..."
        $zipPath = Join-Path $tmp $assetName
        Invoke-Download "$baseUrl/$assetName" $zipPath

        # Verify checksum
        Write-Info "Verifying checksum..."
        $checksumContent = Get-Content $checksumFile -Raw
        $expectedLine = ($checksumContent -split "`n" | Where-Object { $_ -match [regex]::Escape($assetName) } | Select-Object -First 1)
        if (-not $expectedLine) {
            Write-Fatal "Checksum entry for '$assetName' not found in SHA256SUMS. The release may be incomplete."
        }
        $expectedHash = ($expectedLine -split '\s+')[0]
        Test-Checksum $zipPath $expectedHash

        # Extract
        Write-Info "Extracting..."
        $extractDir = Join-Path $tmp "extracted"
        New-Item -ItemType Directory -Path $extractDir | Out-Null
        Expand-Archive -Path $zipPath -DestinationPath $extractDir -Force

        # Determine install directory
        if ($BinDir -eq "") {
            $BinDir = Join-Path $env:LOCALAPPDATA "Cascade\bin"
        }
        New-Item -ItemType Directory -Path $BinDir -Force | Out-Null

        # Install binaries
        $installed = 0
        foreach ($bin in @("cascade.exe", "cascaded.exe")) {
            $src = Join-Path $extractDir $bin
            if (Test-Path $src) {
                Copy-Item $src (Join-Path $BinDir $bin) -Force
                Write-Info "Installed: $(Join-Path $BinDir $bin)"
                $installed++
            }
        }
        if ($installed -eq 0) {
            Write-Fatal "No binaries found in the zip archive. The release asset may be malformed."
        }

        # Add to user PATH (persistent, no Admin required)
        $currentPath = [System.Environment]::GetEnvironmentVariable("Path", "User")
        if ($currentPath -notlike "*$BinDir*") {
            [System.Environment]::SetEnvironmentVariable(
                "Path",
                "$BinDir;$currentPath",
                "User"
            )
            Write-Info "Added $BinDir to user PATH."
            Write-Warn "PATH change takes effect in new terminal windows."
        } else {
            Write-Info "$BinDir is already in user PATH."
        }

        # Also add to current session PATH so post-install steps work.
        $env:Path = "$BinDir;$env:Path"

        # Daemon install
        $cascadeExe = Join-Path $BinDir "cascade.exe"
        if (-not $NoDaemon) {
            Write-Info "Installing daemon service..."
            try {
                & $cascadeExe daemon install
                Write-Info "Daemon installed and started."
            } catch {
                Write-Warn "'cascade daemon install' failed — you may need to start the daemon manually. $_"
            }
        } else {
            Write-Info "Skipping daemon install (NoDaemon flag set)."
        }

        # Init
        if (-not $NoInit) {
            Write-Info "Running cascade init..."
            try {
                & $cascadeExe init --accept-defaults
                Write-Info "Init complete."
            } catch {
                Write-Warn "'cascade init --accept-defaults' failed — run it manually. $_"
            }
        } else {
            Write-Info "Skipping init (NoInit flag set)."
        }

        # Verify
        Write-Info "Running verification..."
        try {
            & $cascadeExe verify
            Write-Info "cascade verify passed."
        } catch {
            Write-Warn "'cascade verify' reported issues — run it again after opening a new terminal."
        }

        Write-Info "Done. Run 'cascade --version' to confirm."

    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}

Main
