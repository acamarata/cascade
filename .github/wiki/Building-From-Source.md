# Building From Source

This page shows how to build the Cascade CLI and daemon from source. For the full development environment including the GUI app and tests, see [Development Setup](Development-Setup.md).

---

## Prerequisites

- Rust stable 1.77+: `rustup update stable`
- Git

That is all you need for the CLI and daemon. The GUI app requires additional prerequisites (Node.js, pnpm, Tauri CLI); see [Development Setup](Development-Setup.md).

---

## Clone the repo

```bash
git clone https://github.com/acamarata/cascade.git
cd cascade
```

---

## Build the CLI

```bash
cargo build --release -p cascade-cli
```

Binary output:
- macOS/Linux: `target/release/cascade`
- Windows: `target\release\cascade.exe`

---

## Build the daemon

```bash
cargo build --release -p cascade-daemon
```

Binary output: `target/release/cascaded`

---

## Build all crates

```bash
cargo build --release
```

---

## Install to PATH

```bash
# Install from local source
cargo install --path crates/cascade-cli

# The binary is placed in ~/.cargo/bin/cascade
# Make sure ~/.cargo/bin is in your PATH
```

---

## Build with debug info

```bash
cargo build -p cascade-cli
# Binary at target/debug/cascade
```

Debug builds are slower to run and larger in size, but include symbol information for debugging.

---

## Cross-compilation

Cascade supports cross-compilation for the main targets. You need the target installed:

```bash
# Install a target
rustup target add aarch64-apple-darwin
rustup target add x86_64-unknown-linux-gnu
rustup target add x86_64-pc-windows-msvc

# Build for a specific target
cargo build --release --target aarch64-apple-darwin -p cascade-cli
```

For Linux cross-compilation from macOS, you will need a cross-linker. The `cross` tool simplifies this:

```bash
cargo install cross
cross build --release --target x86_64-unknown-linux-gnu -p cascade-cli
```

---

## Build the GUI app

The Tauri app requires Node.js 20+, pnpm, and the Tauri CLI. See [Development Setup](Development-Setup.md) for prerequisites.

```bash
cd apps/cascade-app
pnpm install
pnpm tauri build
```

Output bundle location:
- macOS: `apps/cascade-app/src-tauri/target/release/bundle/macos/Cascade.app`
- Linux: `apps/cascade-app/src-tauri/target/release/bundle/deb/`
- Windows: `apps/cascade-app/src-tauri/target/release/bundle/msi/`

---

## Verify the build

```bash
./target/release/cascade --version
./target/release/cascade doctor
```

---

## Checksums

To compare your build against the official release:

```bash
sha256sum target/release/cascade
```

Compare against `checksums.txt` from the [releases page](https://github.com/acamarata/cascade/releases). The checksums should match when built with the same Rust toolchain version from the same commit. See [Code Signing](Code-Signing.md) for details on reproducible builds.

---

See also: [Development Setup](Development-Setup.md) · [Distribution Channels](Distribution-Channels.md) · [Testing](Testing.md)
