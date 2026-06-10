# Distribution Channels

Cascade is available through nine channels. Pick the one that fits your platform and workflow.

---

## Channel summary

| Channel | Package | Platform | Command |
|---|---|---|---|
| Homebrew Cask | `acamarata/tap/cascade` | macOS | `brew install acamarata/tap/cascade` |
| AUR | `cascade-bin` | Linux (Arch) | `yay -S cascade-bin` |
| Winget | `acamarata.cascade` | Windows | `winget install acamarata.cascade` |
| Chocolatey | `cascade` | Windows | `choco install cascade` |
| Scoop | `cascade` | Windows | `scoop install cascade` |
| Snap | `cascade` | Linux | `sudo snap install cascade` |
| Flatpak | `dev.camarata.Cascade` | Linux | `flatpak install dev.camarata.Cascade` |
| Nix | `github:acamarata/cascade` | NixOS / flakes | see below |
| crates.io | `cascade-cli` | any | `cargo install cascade-cli` |

---

## Homebrew (macOS)

The recommended method for macOS. Installs both the GUI app and the CLI binary.

```bash
brew install acamarata/tap/cascade
```

Upgrade:

```bash
brew upgrade acamarata/tap/cascade
```

To install the CLI only (no GUI):

```bash
brew install acamarata/tap/cascade-cli
```

---

## AUR (Arch Linux)

Install the prebuilt binary package with any AUR helper:

```bash
yay -S cascade-bin
# or
paru -S cascade-bin
```

The AUR package installs the CLI binary. The GUI app is not available on Linux in the current release.

---

## Winget (Windows)

```powershell
winget install acamarata.cascade
```

Upgrade:

```powershell
winget upgrade acamarata.cascade
```

---

## Chocolatey (Windows)

```powershell
choco install cascade
```

Upgrade:

```powershell
choco upgrade cascade
```

---

## Scoop (Windows)

```powershell
scoop install cascade
```

Upgrade:

```powershell
scoop update cascade
```

---

## Snap (Linux)

```bash
sudo snap install cascade
```

The snap package runs confined. The daemon uses a Unix socket under the snap data directory.

---

## Flatpak (Linux)

```bash
flatpak install flathub dev.camarata.Cascade
```

Run:

```bash
flatpak run dev.camarata.Cascade
```

The CLI is also available:

```bash
flatpak run dev.camarata.Cascade cascade --help
```

---

## Nix (NixOS and flakes)

Add to your `flake.nix` inputs:

```nix
inputs.cascade.url = "github:acamarata/cascade";
```

Install the CLI:

```nix
environment.systemPackages = [ inputs.cascade.packages.${pkgs.system}.cascade-cli ];
```

Or run it directly:

```bash
nix run github:acamarata/cascade -- --help
```

---

## crates.io (any platform)

Install the CLI from crates.io using Cargo. This requires Rust and Cargo installed.

```bash
cargo install cascade-cli
```

This compiles from source and places the `cascade` binary in `~/.cargo/bin/`.

Upgrade:

```bash
cargo install cascade-cli --force
```

---

## Checking your install

After installing through any channel, verify:

```bash
cascade --version
cascade doctor
```

`cascade doctor` runs a full health check: binary location, daemon socket, config file, and tool detection.

---

## Direct download

Prebuilt binaries for each release are attached to the [GitHub releases page](https://github.com/acamarata/cascade/releases/latest). Download the archive for your platform, extract, and add the binary to your PATH.

Available targets:
- `aarch64-apple-darwin` (macOS Apple Silicon)
- `x86_64-apple-darwin` (macOS Intel)
- `x86_64-unknown-linux-gnu` (Linux x86_64, glibc)
- `aarch64-unknown-linux-gnu` (Linux ARM64, glibc)
- `x86_64-pc-windows-msvc` (Windows x86_64)

---

---

## Verifying signatures

Every Linux release artifact has a detached GPG signature. Every release includes a `checksums.txt` (SHA256) that is also GPG-signed.

**Import the release key:**

```bash
gpg --import https://github.com/acamarata/cascade/raw/main/.github/RELEASE_KEY.asc
```

Or by key fingerprint:

```bash
gpg --recv-keys 3C463D90DF3061AA752FB8500F5729773E694CEA
```

**Verify a Linux artifact:**

```bash
gpg --verify cascade-x86_64-unknown-linux-gnu.tar.gz.asc cascade-x86_64-unknown-linux-gnu.tar.gz
```

**Verify the checksum file:**

```bash
gpg --verify checksums.txt.asc checksums.txt
sha256sum -c checksums.txt
```

Both commands should exit 0 and report `Good signature from "Cascade Release Key"`.

The release key fingerprint `3C463D90DF3061AA752FB8500F5729773E694CEA` is canonical in `SECURITY.md`.

---

See also: [Install](Install.md) · [Building From Source](Building-From-Source.md) · [Development Setup](Development-Setup.md) · [Code Signing](Code-Signing.md)
