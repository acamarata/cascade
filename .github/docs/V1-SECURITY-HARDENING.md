# Cascade v1 — Security & Release Hardening (P10 candidate)

_Generated 2026-06-14 during the pre-release CI review. All 9 feature phases
(P5–P9) are complete and the workspace tests pass with 0 failures. The items
below are the remaining **dependency-security + license** work that gates a
clean public v1, surfaced for a founder decision._

## What's clean
- **gitleaks**: 0 leaks on the tracked tree (the false-positive allowlist for
  `secrets_scanner.rs` + `proxy_client.rs` covers the scanner's own pattern
  literals). No real secret committed. The release private key lives only in the
  OS keychain; only the PUBLIC key is in the repo.
- **Feature code**: workspace `cargo test` 0 failed; app vitest 769; typecheck +
  build clean.

## The residual: dependency advisories + licenses (NOT release-clean yet)

### Advisories — 21 vulnerabilities + 33 unmaintained warnings (RustSec)
| Source | Count | Severity / nature | Fix |
|---|---|---|---|
| **wasmtime 22** | **16** | 2026 batch incl. aarch64 Cranelift sandbox escape (RUSTSEC-2026-0096), heap-OOB, WASI resource exhaustion | **Major upgrade 22 → 35+/45** (large, risky migration of the WASM plugin runtime) |
| rustc-serialize | 2 | unmaintained, stack overflow on nested JSON | transitive — find parent + update/replace |
| atty / gtk-sys / gdk-sys / atk-sys | ~5 | unmaintained, **Linux-tray-only** | update `tray-item` / Linux GUI stack, or gate the indicator |
| 33 misc | warn | unmaintained/yanked transitive | `cargo update` does NOT clear (latest-in-range already); needs targeted bumps |

### Licenses
- **libappindicator-sys** = `LGPL-2.1/LGPL-3.0` — Linux system-tray binding,
  **dynamically linked** to the system lib (LGPL-compliant; no obligation on our
  MIT code). Violates the project's "no LGPL" rule on paper → documented
  exception OR swap the Linux tray backend.
- `ring` has no SPDX `license` field → needs a clarify (ISC AND MIT AND OpenSSL).
- Permissive licenses to allow: MPL-2.0, Unicode-3.0, OpenSSL, CDLA-Permissive-2.0,
  Unlicense (all done in deny.toml).

## The decision (founder)
**Option A — Ship v1 now, harden as v1.0.1:** accept the advisories with
documented mitigations (Cascade runs **user-installed** plugins, not untrusted
multi-tenant code; WASI is capability-gated; fuel + memory limits apply), finish
the deny.toml exceptions so CI is green-with-documented-risk, merge + release.
Fast; ships with known (mitigated) CVEs in the plugin sandbox.

**Option B — Harden first (P10), then release:** do the wasmtime major upgrade
(clears 16/21), clean the unmaintained Linux-GUI + transitive deps, resolve the
LGPL dep, then ship with a genuinely clean security gate. Correct for a
security-positioned FOSS tool; the wasmtime migration is real, risky work that
could destabilize the plugin system and needs full re-validation.

**Recommendation:** Option B (P10) for a credible security tool — but it's a real
phase of work and your call. The wasmtime migration specifically should not be
done blind; it warrants its own focused, validated effort.
