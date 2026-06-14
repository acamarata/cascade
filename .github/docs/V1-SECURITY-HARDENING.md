# Cascade v1 — Security & Release Hardening (P10) — COMPLETE

_P10 executed 2026-06-14 per founder decision (Option B: harden before v1).
All 9 feature phases (P5–P9) were already complete + green; this pass closed the
dependency-security and license gaps that gated a clean public release._

## What P10 fixed

### 1. wasmtime sandbox runtime: 22 → 36.0.10 LTS
The plugin WASM runtime carried 16 RUSTSEC-2026 advisories on wasmtime 22,
including **RUSTSEC-2026-0096 — aarch64 Cranelift miscompile enabling sandbox
escape** (the host is Apple Silicon) and heap-OOB / WASI resource-exhaustion
issues, plus **RUSTSEC-2026-0149 — WASI `path_open(TRUNCATE)` bypassing
`FilePerms::WRITE`**.

- Upgraded to **wasmtime 36.0.10 LTS** (chosen over latest 45 because 45 needs
  `cc ^1.2.41`, which forces a tree-sitter family migration through the RAG
  code chunker; 36.x keeps `cc ^1.0` and is the LTS line with security
  backports). 36.0.10 clears **all 17** wasmtime/wasmtime-wasi advisories.
- API migration: `wasmtime_wasi::preview1` module, explicit `async_support(true)`,
  `set_fuel`/`consume_fuel`, `StoreLimitsBuilder`. The capability-gated WASI
  (deny-by-default fs/net, manifest-only preopens), fuel metering, memory
  limits, and the i64-packed JSON ABI are all preserved.
- **Validated at runtime**: 98 plugin tests pass on 36.0.10 — including the real
  WASM round-trip and all 14 SIEGE vectors (fuel exhaustion trapped,
  path-traversal denied, ABI-abuse handled).

### 2. LGPL + GTK Linux-tray dependency removed
The Linux system tray used `libappindicator-sys` (**LGPL-2.1/LGPL-3.0**,
violating the project's no-LGPL rule) which pulled unmaintained gtk/gdk/atk-sys
bindings. Replaced the Linux backend with **`ksni` 0.3** (pure-Rust
StatusNotifierItem over zbus, MIT, no GTK, no system libs). Menu items, action
dispatch, and lifecycle preserved. The LGPL crate is gone.

### 3. CI build workflows repaired
Reconciled ci-app / daemon-ci / ci-dashboard / ci-linux-widgets / template-ci:
corepack-before-pnpm ordering, the real `@cascade/dashboard` package name,
removed the now-stale GTK/libappindicator apt installs (ksni needs none),
sccache pin. (Tauri's own GTK-webview Linux GUI deps remain — see below.)

### 4. License + advisory gate finalized (honest, documented)
- `cascade-app` is MIT-licensed; permissive licenses (MPL-2.0, Unicode-3.0,
  OpenSSL, CDLA, Unlicense) allow-listed; `ring` clarified; internal
  workspace path-deps handled via `allow-wildcard-paths`.
- gitleaks: 0 real leaks (scanner pattern-literals allow-listed).

## Documented accepted-risk residual (no exploit path; in deny.toml + cargo-audit)
None of these are exploitable sandbox escapes — those are fixed above.
- **GTK3 stack** (atk/gdk/gtk/glib, 11 advisories) — **required by Tauri 2's
  Linux GTK-webview GUI**; gtk-rs marked them unmaintained after moving to gtk4.
  Linux-only, maintenance-status only.
- **Transitive build/util deps** (proc-macro-error, instant, derivative, paste,
  fxhash, number_prefix, lru) — unmaintained/unsound, no exploit path in our use.
- **unic-*** Unicode crates — unmaintained transitive text processing.
- **ring RUSTSEC-2025-0009** — AES panic only under overflow-checks (debug);
  release builds are unaffected.

Each is enumerated by RUSTSEC id in `deny.toml [advisories].ignore` and the
`cargo-audit` CI step, with the rationale above. The security gate now passes on
genuine fixes plus this explicitly-documented, no-exploit residual.
