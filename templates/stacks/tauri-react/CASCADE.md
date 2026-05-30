# Stack Template: Tauri 2 + React

**Tier:** APC · **Stack:** Tauri 2 (Rust backend) + React/Vite (frontend) · **Languages:** Rust + TypeScript strict

## Idiomatic Layout

```
src-tauri/
  src/
    commands/           # Tauri IPC command handlers (#[tauri::command])
    services/           # Business logic called by commands
    models/             # Data structs (serde Serialize/Deserialize)
    config.rs           # App configuration
    lib.rs              # Plugin registration, app builder
    main.rs             # Entry point (thin)
  tauri.conf.json       # Permissions, bundle config, updater
  Cargo.toml
src/                    # React/Vite frontend (same layout as react-vite)
  components/
  hooks/
  lib/
  services/
    tauri.ts            # Typed invoke() wrappers
  types/
  pages/
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- IPC boundary: every Tauri command in `commands/`; business logic in `services/`; never inline logic in commands
- Frontend `services/tauri.ts` wraps all `invoke()` calls with typed signatures
- Rust commands return `Result<T, String>`; frontend handles errors uniformly
- Updater config in `tauri.conf.json`; signing keys in vault

## Key Commands

```bash
pnpm tauri dev          # Dev mode (Rust + Vite concurrent)
pnpm tauri build        # Production build + bundle
cargo test              # Rust unit tests
cargo clippy            # Rust linter
pnpm typecheck          # TypeScript check
pnpm test               # Frontend tests (Vitest)
pnpm tauri info         # Dependency diagnostics
```

## Engineering Rules

- Rust edition 2021; `clippy::all` warnings as errors in CI
- Tauri capabilities: minimum required permissions in `tauri.conf.json`
- Code signing: certificate paths in vault; never hardcoded
- File ceiling: Rust modules ≤400 lines; React components ≤200 lines

## Cross-Refs

- `.cascade/rules/frontend-stack-selection.md`
- `.cascade/rules/credentials-vault.md`
- `.cascade/rules/engineering-excellence.md`
