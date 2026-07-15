# Installation

Cascade runs on macOS, Linux, and Windows. Choose the method that fits your setup.

After any install, run `cascade --version` to confirm the CLI is available.

---

## Quick install (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
```

The script:

1. Detects macOS or Linux and the arm64 or x86\_64 architecture.
2. Resolves the latest GitHub release, or uses `CASCADE_VERSION` when set.
3. Downloads the matching release archive and `SHA256SUMS` file.
4. Verifies the archive's SHA-256 checksum before installing it.
5. Installs the official archive's `cascade` and `cascaded` binaries to `~/.local/bin` (or `CASCADE_BIN_DIR`).
6. Prints PATH instructions when the install directory is not already on PATH.
7. Runs `cascade daemon install` unless `CASCADE_NO_DAEMON` is set.
8. Runs `cascade init --accept-defaults` in the directory from which the installer was invoked unless `CASCADE_NO_INIT` is set.
9. Always runs `cascade verify`; verification problems are reported as warnings so installation can finish.

Only the daemon-registration and initialization steps are skippable. The verification step always runs.

**Requirements:** `curl` or `wget`, `tar`, and `sha256sum` or `shasum`. No `sudo` is required.

---

## Quick install (Windows)

```powershell
irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex
```

The PowerShell script follows the same download, checksum, daemon, initialization, and verification flow. It supports x86\_64 Windows, installs to `%LOCALAPPDATA%\Cascade\bin` by default, adds that directory to the current user's PATH, and does not require Administrator access. Open a new terminal to pick up a newly-added PATH entry.

**Requirements:** x86\_64 Windows and PowerShell 5.1 or later.

---

## Installer environment variables

Both quick-install scripts respect these variables.

| Variable | Default | Effect |
|---|---|---|
| `CASCADE_VERSION` | latest release | Install a specific tag, for example `v1.16.0`. |
| `CASCADE_BIN_DIR` | `~/.local/bin` (POSIX) / `%LOCALAPPDATA%\Cascade\bin` (Windows) | Override the install directory. |
| `CASCADE_NO_DAEMON` | unset | Skip `cascade daemon install`. |
| `CASCADE_NO_INIT` | unset | Skip `cascade init --accept-defaults`. |

`CASCADE_NO_DAEMON` and `CASCADE_NO_INIT` may contain any non-empty value.

---

## Package managers

The package-manager commands advertised in the repository README are:

| Platform | Command |
|---|---|
| macOS (Homebrew) | `brew install --cask acamarata/cascade/cascade` |
| Arch Linux (AUR) | `yay -S cascade-bin` |
| Linux (Snap) | `snap install cascade` |
| Linux (Flatpak) | `flatpak install flathub dev.camarata.Cascade` |
| Windows (Winget) | `winget install acamarata.cascade` |
| Windows (Chocolatey) | `choco install cascade` |
| Windows (Scoop) | `scoop install cascade` |
| Any Rust-supported platform (CLI only) | `cargo install cascade-cli` |

For Scoop, add the project bucket first if it is not already configured:

```powershell
scoop bucket add cascade https://github.com/acamarata/scoop-cascade
scoop install cascade
```

Package-manager publication can lag behind a GitHub release. Use the quick-install script or build from source when a channel does not yet carry the desired version.

`cargo install cascade-cli` installs the `cascade` CLI only. Daemon commands that start or register `cascaded` also require the daemon binary from a release archive or a source build.

---

## Build from source

Install a stable Rust toolchain from [rustup.rs](https://rustup.rs), then build both binaries:

```sh
git clone https://github.com/acamarata/cascade.git
cd cascade
cargo build --release -p cascade-cli -p cascade-daemon
```

The binaries are written to `target/release/cascade` and `target/release/cascaded`. Copy both onto your PATH before registering the service:

```sh
mkdir -p ~/.local/bin
install -m 755 target/release/cascade ~/.local/bin/cascade
install -m 755 target/release/cascaded ~/.local/bin/cascaded
```

Then initialize the intended directory and verify it:

```sh
cascade daemon install
cascade init /path/to/project --accept-defaults
cascade verify --dir /path/to/project
```

---

## Verify the install

```sh
cascade --version
cascade verify
```

`cascade verify` checks the active AI folder, cascade resolution, daemon socket, provider availability, configuration syntax, and OS keychain access. It exits successfully when no check is `FAIL`; a stopped daemon is only a warning unless `--require-daemon` is used.

A new install without a connected cloud provider or local model can fail the provider check. Configure one with `cascade provider add --kind <PROVIDER> --api-key <KEY>` (or install a local model), then rerun `cascade verify`. Run `cascade doctor` for a broader diagnostic report.

---

See also: [Home](Home.md) · [Quickstart](Quickstart.md) · [CLI Reference](CLI-Reference.md) · [Troubleshooting](Troubleshooting.md)
