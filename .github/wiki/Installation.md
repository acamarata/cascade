# Installation

Cascade runs on macOS, Linux, and Windows. Choose the method that fits your setup.

After any install, run `cascade --version` to confirm it worked.

---

## Quick install (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
```

The script:

1. Detects your OS and architecture (arm64 / x86\_64).
2. Resolves the latest release from the GitHub API (or uses `CASCADE_VERSION` if set).
3. Downloads the release tarball from GitHub Releases.
4. Verifies the SHA-256 checksum before writing anything.
5. Installs `cascade` and `cascaded` to `~/.local/bin` (or `CASCADE_BIN_DIR`).
6. Runs `cascade daemon install` to register the background service.
7. Runs `cascade init --accept-defaults` to scaffold your global config.
8. Runs `cascade verify` to confirm the setup works.

Steps 6–8 are skippable via environment variables — see below.

**Requirements:** `curl` or `wget`, `tar`, `sha256sum` or `shasum`. No `sudo` needed.

---

## Quick install (Windows)

```powershell
irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex
```

The script performs the same steps as the POSIX version. It installs to `%LOCALAPPDATA%\Cascade\bin` by default and does not require Administrator.

**Requirements:** PowerShell 5.1 or later (ships with Windows 10 and later).

---

## Environment variables

Both scripts respect these variables.

| Variable | Default | Effect |
|---|---|---|
| `CASCADE_VERSION` | latest release | Install a specific tag, e.g. `v1.0.0`. |
| `CASCADE_BIN_DIR` | `~/.local/bin` (POSIX) / `%LOCALAPPDATA%\Cascade\bin` (Windows) | Override the install directory. |
| `CASCADE_NO_DAEMON` | unset | Skip `cascade daemon install`. |
| `CASCADE_NO_INIT` | unset | Skip `cascade init --accept-defaults`. |

---

## Homebrew (macOS)

A Homebrew cask is published alongside each release.

```sh
brew install --cask acamarata/cascade/cascade
```

The cask installs a macOS application bundle and wires `cascade` into your PATH via the bundle binary. If the tap is not yet available for your version, use the curl one-liner above or build from source.

---

## Scoop (Windows)

A Scoop manifest is published alongside each release.

```powershell
scoop bucket add cascade https://github.com/acamarata/scoop-cascade
scoop install cascade
```

If the bucket is not yet available for your version, use the PowerShell one-liner above or build from source.

---

## AUR (Arch Linux)

The `cascade-bin` package targets the Arch User Repository.

```sh
yay -S cascade-bin
```

The package pulls pre-built binaries from GitHub Releases and installs a systemd user unit (`cascade@.service`) for the background daemon.

---

## Build from source

You need a Rust stable toolchain. Install one from [rustup.rs](https://rustup.rs) if needed.

```sh
git clone https://github.com/acamarata/cascade.git
cd cascade
cargo build --release -p cascade-cli
```

The compiled binary lands at `target/release/cascade`. Copy it onto your PATH:

```sh
install -m 755 target/release/cascade ~/.local/bin/cascade
```

Then run the post-install steps:

```sh
cascade daemon install
cascade init --accept-defaults
cascade verify
```

---

## Verify the install

After any install method:

```sh
cascade --version
cascade verify
```

`cascade verify` runs a checklist of setup requirements and exits 0 only when everything passes. Run `cascade doctor` if anything fails — it prints a detailed report with suggested fixes.

---

See also: [Quickstart](Quickstart.md) · [CLI Reference](CLI-Reference.md) · [Troubleshooting](Troubleshooting.md)
