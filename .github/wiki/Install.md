# Install

Cascade runs on macOS, Linux, and Windows. Pick the method that fits your workflow.

## Prerequisites

| Requirement | Version | Notes |
|---|---|---|
| Operating system | macOS 13+, Ubuntu 22.04+, Windows 10+ | Older versions may work but are untested |
| Disk space | ~50 MB | Plus index storage (varies by codebase size) |
| Internet | Initial install only | All subsequent operations are local |

No runtime dependencies are required after install. The binary is self-contained.

---

## macOS

### Homebrew (recommended)

```bash
brew install acamarata/tap/cascade
```

Verify:

```bash
cascade --version
```

### Direct download (DMG)

1. Download `cascade-v0.1.0-macos-universal.dmg` from the [releases page](https://github.com/acamarata/cascade/releases/latest).
2. Open the DMG and drag `Cascade.app` to `/Applications`.
3. Launch from Applications or run `cascade` from your terminal (the app adds the CLI to your PATH on first launch).

The DMG is signed and notarized. macOS will not show a security warning.

---

## Linux

### AUR (Arch Linux)

```bash
yay -S cascade-bin
# or: paru -S cascade-bin
```

Manual AUR install:

```bash
git clone https://aur.archlinux.org/cascade-bin.git
cd cascade-bin
makepkg -si
```

### Debian / Ubuntu (.deb)

```bash
wget https://github.com/acamarata/cascade/releases/latest/download/cascade_0.1.0_amd64.deb
sudo dpkg -i cascade_0.1.0_amd64.deb
```

### Fedora / RHEL (.rpm)

```bash
sudo rpm -i https://github.com/acamarata/cascade/releases/latest/download/cascade-0.1.0.x86_64.rpm
```

### AppImage (any distro)

```bash
wget https://github.com/acamarata/cascade/releases/latest/download/cascade-0.1.0-x86_64.AppImage
chmod +x cascade-0.1.0-x86_64.AppImage
./cascade-0.1.0-x86_64.AppImage
```

For system-wide use, move the AppImage to `~/.local/bin/cascade` and add that directory to your `PATH`.

### Snap

```bash
sudo snap install cascade
```

### Flatpak

```bash
flatpak install flathub io.acamarata.cascade
```

---

## Windows

### Winget (recommended)

```powershell
winget install acamarata.cascade
```

### Chocolatey

```powershell
choco install cascade
```

### Scoop

```powershell
scoop bucket add acamarata https://github.com/acamarata/scoop-bucket
scoop install cascade
```

### MSI installer

Download `cascade-0.1.0-x64.msi` from the [releases page](https://github.com/acamarata/cascade/releases/latest) and run the installer. Cascade will be added to your PATH automatically.

---

## cargo install (any platform with Rust)

If you have Rust installed:

```bash
cargo install cascade-cli
```

This compiles from source. Minimum supported Rust version (MSRV): 1.78.

---

## Verify your install

Regardless of method:

```bash
cascade --version
# cascade 0.1.0

cascade doctor
# Checks prerequisites and configuration; reports any issues
```

If `cascade` is not found after install, check that its install location is in your `PATH`. Run `cascade doctor` first before filing a bug report — it diagnoses the most common setup issues automatically.

---

## Uninstall

| Method | Command |
|---|---|
| Homebrew | `brew uninstall cascade` |
| AUR | `yay -R cascade-bin` |
| .deb | `sudo dpkg -r cascade` |
| .rpm | `sudo rpm -e cascade` |
| Winget | `winget uninstall acamarata.cascade` |
| Chocolatey | `choco uninstall cascade` |
| cargo | `cargo uninstall cascade-cli` |

User data (your cascade files and index) is stored in `~/.cascade/` and is not removed by uninstall. Delete that directory manually if you want a clean slate.
