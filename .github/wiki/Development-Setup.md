# Development Setup

This page walks through setting up a local development environment for the Cascade Rust workspace. You need this to build from source, run tests, or contribute changes.

---

## Prerequisites

| Tool | Minimum version | Purpose |
|---|---|---|
| Rust + Cargo | 1.77+ (stable) | Build the Rust workspace |
| pnpm | 9+ | Manage the Tauri app's JS dependencies |
| Node.js | 20+ | Tauri app build toolchain |
| SQLite | 3.39+ | SQLite FTS5 and sqlite-vec (usually bundled) |
| Git | 2.x | Source control |

Optional:

| Tool | Purpose |
|---|---|
| `cargo-watch` | Auto-rebuild on file changes |
| `cargo-nextest` | Faster test runner (drop-in replacement for `cargo test`) |
| Tauri CLI | Needed to build the desktop GUI app |

---

## Install Rust

Use rustup. Do not use a system Rust package; it is often outdated.

```bash
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
source ~/.cargo/env
rustup update stable
rustup target add wasm32-wasip1     # for building WASM plugins
```

---

## Install pnpm and Node

The recommended method is corepack (bundled with Node.js):

```bash
# macOS via Homebrew
brew install node

# Linux via nvm
curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.7/install.sh | bash
nvm install 20

# Enable corepack (activates pnpm)
corepack enable
```

---

## Install Tauri prerequisites

### macOS

The macOS SDK is required. Install Xcode Command Line Tools:

```bash
xcode-select --install
```

Then install the Tauri CLI via Cargo:

```bash
cargo install tauri-cli --version "^2"
```

### Linux

Install build dependencies:

```bash
# Ubuntu / Debian
sudo apt-get install -y libwebkit2gtk-4.1-dev build-essential curl wget file \
    libssl-dev libayatana-appindicator3-dev librsvg2-dev

# Arch
sudo pacman -Sy webkit2gtk-4.1 base-devel curl wget file openssl \
    appmenu-gtk-module gtk3 libappindicator-gtk3 librsvg
```

Then install the Tauri CLI:

```bash
cargo install tauri-cli --version "^2"
```

### Windows

Install Visual Studio 2022 with the "Desktop development with C++" workload, then:

```powershell
cargo install tauri-cli --version "^2"
```

---

## Clone and build

```bash
git clone https://github.com/acamarata/cascade.git
cd cascade

# Build all crates
cargo build

# Build release binaries
cargo build --release

# The CLI binary ends up at:
# target/release/cascade (macOS/Linux)
# target\release\cascade.exe (Windows)
```

---

## Run the tests

```bash
# All unit and integration tests
cargo test

# Faster with nextest (install once: cargo install cargo-nextest)
cargo nextest run

# A single crate
cargo test -p cascade-rag

# A specific test
cargo test -p cascade-cli test_init_command
```

---

## Build the Tauri GUI app

The GUI app lives in `apps/cascade-app/`. It requires pnpm for its JS dependencies.

```bash
cd apps/cascade-app
pnpm install
pnpm tauri build        # production build
pnpm tauri dev          # development mode with hot reload
```

---

## Development workflow

Use `cargo-watch` to auto-rebuild when files change:

```bash
cargo install cargo-watch
cargo watch -x "build -p cascade-cli"
```

Or run the daemon in the foreground with debug logging:

```bash
RUST_LOG=debug cargo run -p cascade-daemon
```

---

## Workspace structure

```
cascade/
├── crates/
│   ├── cascade-types/     # shared traits and types (Chunker, Retriever, etc.)
│   ├── cascade-core/      # cascade resolver, cascade resolution engine
│   ├── cascade-rag/       # RAG indexer (FTS5 + BGE-M3 + RRF)
│   ├── cascade-mcp/       # MCP server (5 transports)
│   ├── cascade-cli/       # cascade binary (Clap CLI)
│   ├── cascade-daemon/    # cascaded daemon (Tokio)
│   └── cascade-plugins/   # WASM plugin host (wasmtime)
├── apps/
│   └── cascade-app/       # Tauri 2 desktop GUI
├── scripts/               # build and release scripts
└── bench/                 # Criterion benchmarks
```

---

## Environment variables

| Variable | Purpose |
|---|---|
| `RUST_LOG` | Log verbosity (e.g. `debug`, `trace`, `cascade_daemon=debug`) |
| `CASCADE_SOCKET` | Override the daemon socket path |
| `CASCADE_CONFIG` | Override the config file path |

---

## Lint and format

```bash
cargo fmt                          # format all code
cargo fmt --check                  # check without writing
cargo clippy -- -D warnings        # lint, fail on any warning
```

CI runs all three. A PR with lint warnings or formatting issues will fail.

---

See also: [Building From Source](Building-From-Source.md) · [Testing](Testing.md) · [Contributing](Contributing.md)
