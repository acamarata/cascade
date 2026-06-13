# Installation

Cascade runs on macOS, Linux, and Windows. This page covers every install path including the unattended/agent flow.

For platform-specific package manager options, see [Install](Install.md). For building from source, see [Building From Source](Building-From-Source.md).

---

## One-liner install (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
```

What it does:

1. Detects your OS (macOS / Linux) and architecture (arm64 / x86_64).
2. Resolves the latest release tag from the GitHub API (or uses `CASCADE_VERSION`).
3. Downloads the matching release tarball from GitHub Releases.
4. Downloads `SHA256SUMS` and verifies the checksum before touching anything.
5. Extracts `cascade` and `cascaded` binaries.
6. Installs them to `~/.local/bin` (or `CASCADE_BIN_DIR`).
7. Ensures the directory is on your `PATH` (prints advice if not).
8. Runs `cascade daemon install`.
9. Runs `cascade init --accept-defaults`.
10. Runs `cascade verify`.

Steps 8–10 are individually skippable via environment variables (see below).

### Requirements

- `curl` or `wget`
- `tar`
- `sha256sum` or `shasum`
- No `sudo` required.

---

## One-liner install (Windows)

```powershell
irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex
```

What it does:

1. Detects architecture (x86_64 only; ARM64 Windows is not yet in the release matrix).
2. Resolves the latest release tag from the GitHub API (or uses `CASCADE_VERSION`).
3. Downloads `cascade-{VERSION}-x86_64-pc-windows-msvc.zip` from GitHub Releases.
4. Downloads `SHA256SUMS` and verifies the checksum before extraction.
5. Extracts `cascade.exe` and `cascaded.exe`.
6. Installs them to `%LOCALAPPDATA%\Cascade\bin` (or `CASCADE_BIN_DIR`).
7. Adds the directory to the current user's `PATH` (no Administrator required).
8. Runs `cascade daemon install`.
9. Runs `cascade init --accept-defaults`.
10. Runs `cascade verify`.

### Requirements

- PowerShell 5.1+ (ships with Windows 10 and later)
- No Administrator prompt required.

---

## Environment variables

Both scripts honour the same set of environment variables.

| Variable | Default | Purpose |
|---|---|---|
| `CASCADE_VERSION` | latest release | Pin a specific release tag (e.g. `v1.0.0`). |
| `CASCADE_BIN_DIR` | `~/.local/bin` (sh) / `%LOCALAPPDATA%\Cascade\bin` (ps1) | Override the install directory. |
| `CASCADE_NO_DAEMON` | unset | Set to any non-empty value to skip `cascade daemon install`. |
| `CASCADE_NO_INIT` | unset | Set to any non-empty value to skip `cascade init --accept-defaults`. |

---

## Unattended / CI / agent install

For headless machines, CI runners, and automated agent bootstraps:

```sh
# macOS / Linux — no daemon, no interactive init
CASCADE_NO_DAEMON=1 CASCADE_NO_INIT=1 \
  curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh

# Then manually init from config
cascade init --accept-defaults
cascade daemon install
cascade verify
```

```powershell
# Windows — no daemon, no interactive init
$env:CASCADE_NO_DAEMON = "1"
$env:CASCADE_NO_INIT   = "1"
irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex

# Then manually init from config
cascade init --accept-defaults
cascade daemon install
cascade verify
```

### Custom install directory (e.g. system-wide for all users)

```sh
CASCADE_BIN_DIR=/usr/local/bin \
  curl -fsSL .../install.sh | sh
```

Note: writing to `/usr/local/bin` may require `sudo` on some systems. The script itself never calls `sudo` — prefix the pipeline with `sudo -E sh -c '...'` only if your target directory requires it.

---

## Checksum verification

The installer always verifies checksums before executing anything. If the hash does not match, the script prints the expected and actual digests and exits with a non-zero status. The binaries are never placed on disk on a mismatch.

You can also verify manually:

```sh
# Download SHA256SUMS from the release page
curl -fsSL https://github.com/acamarata/cascade/releases/latest/download/SHA256SUMS -o SHA256SUMS

# Verify your tarball
sha256sum --check SHA256SUMS 2>&1 | grep cascade
```

The `SHA256SUMS` file is also GPG-signed when `GPG_PRIVATE_KEY` is set in the release workflow (see `SHA256SUMS.asc` on releases where signing is configured). The signing key is published at `.github/RELEASE_KEY.asc`.

---

## Release asset names

The installer maps platform to asset using the naming pattern from `.github/workflows/release.yml`:

| Platform | Asset |
|---|---|
| macOS (universal arm64+x86_64) | `cascade-{VERSION}-universal-apple-darwin.tar.gz` |
| macOS arm64 (fallback) | `cascade-{VERSION}-aarch64-apple-darwin.tar.gz` |
| macOS x86_64 (fallback) | `cascade-{VERSION}-x86_64-apple-darwin.tar.gz` |
| Linux x86_64 | `cascade-{VERSION}-x86_64-unknown-linux-gnu.tar.gz` |
| Linux arm64 | `cascade-{VERSION}-aarch64-unknown-linux-gnu.tar.gz` |
| Windows x86_64 | `cascade-{VERSION}-x86_64-pc-windows-msvc.zip` |

The macOS installer prefers the universal binary and falls back to the arch-specific tarball with a clear warning.

---

## Verify the install

After any install method:

```sh
cascade --version
cascade doctor
cascade verify
```

`cascade doctor` checks prerequisites and configuration and reports issues. Run it first before filing a bug report.

---

## Post-install steps

```sh
# Start editing your global rules
cascade edit --tier gci

# Sync derived files to connected tools
cascade sync

# Search your indexed rule base
cascade search "authentication"
```

---

## Uninstall

| Method | Command |
|---|---|
| Script install (macOS/Linux) | `rm ~/.local/bin/cascade ~/.local/bin/cascaded && cascade daemon uninstall` |
| Script install (Windows) | `Remove-Item "$env:LOCALAPPDATA\Cascade\bin\cascade.exe","$env:LOCALAPPDATA\Cascade\bin\cascaded.exe"` |
| Homebrew | `brew uninstall cascade` |
| Winget | `winget uninstall acamarata.cascade` |
| cargo | `cargo uninstall cascade-cli` |

User data (cascade files, RAG index) is stored in `~/.cascade/` (macOS/Linux) or `%APPDATA%\cascade\` (Windows) and is not removed by any uninstall command. Delete that directory manually for a clean slate.

---

See also: [Install](Install.md) · [Building From Source](Building-From-Source.md) · [Quickstart](Quickstart.md) · [Troubleshooting](Troubleshooting.md)
