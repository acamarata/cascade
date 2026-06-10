---
id = "tauri-2"
version = "1.0.0"
tier = "any"
stacks = ["tauri-2"]
project_shapes = []
description = "Tauri 2 conventions: Rust backend, React+Vite frontend, IPC patterns, capability model, and common pitfalls."
---

## File Layout

Tauri 2 is a Rust workspace with a separate frontend. Keep them in `src-tauri/` and `src/` respectively.

```
src/                    # React+Vite frontend (mirrors react-vite layout)
  components/
  hooks/
  lib/
  services/
    tauri.ts            # typed wrappers around invoke() calls
  App.tsx
  main.tsx
src-tauri/
  src/
    main.rs             # Tauri builder — do not put business logic here
    commands/           # one file per command group
      mod.rs
      window.rs
      settings.rs
    state/              # AppState and sub-state structs
      mod.rs
    lib.rs              # re-exports for test accessibility
  capabilities/         # Tauri 2 capability JSON files
  tauri.conf.json       # main Tauri config
  Cargo.toml
Cargo.toml              # workspace root (if monorepo)
```

Rust commands are registered in `main.rs` via `.invoke_handler(tauri::generate_handler![...])`. Keep `main.rs` to wiring only — logic belongs in `commands/`.

## Build Tooling

```bash
pnpm tauri dev        # Vite dev server + Rust daemon with hot-reload
pnpm tauri build      # production app bundle (dmg / msi / deb)
pnpm dev              # frontend-only (no Tauri shell) — for UI-only work
pnpm build            # frontend-only production build
cargo build -p <crate>  # build specific Rust crate
cargo test -p <crate>   # test specific Rust crate
```

Pin `{{TAURI_VERSION}}` in `Cargo.toml` (workspace) and `src-tauri/Cargo.toml`. Pin `@tauri-apps/api` in `package.json` to the same minor version as the Rust crate.

## IPC Patterns

Commands follow a consistent naming convention: `snake_case` in Rust, exposed as `camelCase` to the frontend via Tauri's automatic conversion.

**Rust command definition:**

```rust
// src-tauri/src/commands/settings.rs

use serde::{Deserialize, Serialize};
use tauri::State;

use crate::state::AppState;

/// Response type — always a named struct, never a raw tuple.
#[derive(Debug, Serialize, Deserialize)]
pub struct GetSettingsResponse {
    pub theme: String,
    pub language: String,
}

/// Error type — implement std::fmt::Display for JS error messages.
#[derive(Debug, Serialize, Deserialize, thiserror::Error)]
pub enum SettingsError {
    #[error("settings file not found")]
    NotFound,
    #[error("io error: {0}")]
    Io(String),
}

/// Tauri command — the public IPC surface.
/// Name convention: verb_noun (get_settings, save_settings, delete_entry).
#[tauri::command]
pub async fn get_settings(state: State<'_, AppState>) -> Result<GetSettingsResponse, SettingsError> {
    let settings = state.settings.lock().await;
    Ok(GetSettingsResponse {
        theme: settings.theme.clone(),
        language: settings.language.clone(),
    })
}
```

**Frontend invoke wrapper (TypeScript):**

```ts
// src/services/tauri.ts
import { invoke } from "@tauri-apps/api/core";

interface GetSettingsResponse {
  theme: string;
  language: string;
}

/** Typed wrapper — never call invoke() directly in components. */
export async function getSettings(): Promise<GetSettingsResponse> {
  return invoke<GetSettingsResponse>("get_settings");
}
```

Always define matching TypeScript types for every command's request and response. Keep a single `src/services/tauri.ts` file as the boundary — components call service functions, not `invoke()` directly.

**Error handling contract:** Tauri surfaces Rust `Err` variants as rejected promises. The TypeScript type for the error is `string` (Tauri serialises the `Display` impl). Catch errors in the service layer and convert to typed frontend errors before returning to components.

## Capability Model

Tauri 2 uses a capability-based permission system. Define capabilities in `src-tauri/capabilities/*.json`. Never grant `core:default` broadly — grant the minimum set of permissions the app actually needs.

```json
{
  "$schema": "../gen/schemas/desktop-schema.json",
  "identifier": "main-window",
  "description": "Main window capabilities",
  "windows": ["main"],
  "permissions": [
    "core:window:allow-close",
    "core:window:allow-minimize",
    "fs:allow-read-text-file",
    "fs:scope-app-data"
  ]
}
```

Review capabilities in every PR that adds a new command. Adding `fs:allow-*` permissions without a scoped path is a security issue.

## Testing Convention

```bash
pnpm test              # Vitest — frontend unit tests
pnpm test:e2e          # Playwright + tauri-driver for integration
cargo test -p cascade-core  # Rust unit tests
```

Mock `@tauri-apps/api` in Vitest with `vi.mock("@tauri-apps/api/core", ...)` — never run real Tauri IPC in Vitest. Real IPC is tested in Playwright E2E tests only.

## Common Pitfalls

- **Blocking the async runtime.** Tauri commands run on Tokio. Never call `std::thread::sleep` or blocking I/O inside a `#[tauri::command]` — use `tokio::time::sleep` and `tokio::fs` instead.
- **State not `Send + Sync`.** Any type stored in Tauri `State` must be `Send + Sync`. Wrap non-Send types in `Arc<Mutex<T>>` or `Arc<tokio::sync::Mutex<T>>`.
- **Mismatched Tauri API versions.** The `@tauri-apps/api` JS package major version must match the `tauri` Rust crate major version. A mismatch causes silent IPC failures.
- **Large serialised payloads.** IPC is not a streaming channel. For large data (file contents, search results), chunk the response or use Tauri events instead of commands.
- **Window label hardcoding.** Use `tauri::Manager::get_window` by label. If you rename a window in `tauri.conf.json`, update every place the label is hardcoded in Rust and TS.

## Performance Notes

The Rust backend is the right place for CPU-bound work — offload heavy computation from the frontend. Use `spawn_blocking` for CPU-intensive synchronous code so it does not starve the Tokio runtime.

Frontend bundle size matters even in a desktop app — the WebView has a fixed memory budget. Apply the same code-splitting rules as the react-vite template.

Build release bundles with `pnpm tauri build` and test them on a clean machine before each release. Debug builds are not representative of production performance.
