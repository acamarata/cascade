---
id = "rust-crate"
version = "1.0.0"
tier = "any"
stacks = ["rust-crate"]
project_shapes = []
description = "Rust library crate conventions: doc-tests, public API design, no_std compatibility, and common pitfalls."
---

## File Layout

```
src/
  lib.rs              # crate root — module declarations + public re-exports
  error.rs            # crate-wide error type (thiserror)
  types.rs            # shared data types
  <module>/
    mod.rs            # public items re-exported upward
    impl.rs           # implementation detail (pub(crate) or private)
tests/
  integration.rs      # integration tests — use the public API only
benches/
  <name>.rs           # Criterion benchmarks (if needed)
examples/
  basic.rs            # runnable example for documentation
Cargo.toml
```

`lib.rs` re-exports everything the public API exposes. Internal items are `pub(crate)`. Avoid deep re-export chains — users should be able to `use crate_name::TypeName` without navigating nested modules.

## Build Tooling

```bash
cargo build                    # debug build
cargo build --release          # release build
cargo test                     # all tests (unit + integration + doc-tests)
cargo test --doc               # doc-tests only
cargo doc --no-deps --open     # generate and open documentation
cargo clippy -- -D warnings    # lints as errors
cargo fmt --check              # formatting check (CI gate)
cargo bench                    # run Criterion benchmarks (if present)
```

Pin the minimum supported Rust version (MSRV) in `Cargo.toml`:

```toml
[package]
rust-version = "{{RUST_MSRV}}"
```

Test against MSRV in CI with `rustup toolchain install {{RUST_MSRV}}`.

## Testing Convention

Rust has three test surfaces — use all three:

**Unit tests** (in-file, private access):
```rust
// src/types.rs
#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip_serialization() {
        let v = MyType::new(42);
        let s = serde_json::to_string(&v).unwrap();
        let back: MyType = serde_json::from_str(&s).unwrap();
        assert_eq!(v, back);
    }
}
```

**Doc-tests** (in rustdoc comments — these ARE the usage examples):
```rust
/// Parse a value from a string.
///
/// # Examples
///
/// ```
/// use my_crate::parse;
///
/// let v = parse("42").unwrap();
/// assert_eq!(v, 42);
/// ```
pub fn parse(s: &str) -> Result<u32, ParseError> { ... }
```

**Integration tests** (`tests/` — public API only):
```rust
// tests/integration.rs
use my_crate::MyType;

#[test]
fn public_api_end_to_end() {
    let t = MyType::from_str("hello").expect("valid input");
    assert!(t.is_valid());
}
```

Doc-tests are the most important for a library — they double as live documentation and break immediately if the API changes.

## Lint & Format

```toml
# .clippy.toml or Cargo.toml [lints]
[lints.clippy]
pedantic = "warn"
nursery = "warn"
```

Run `cargo clippy -- -D warnings` in CI. All clippy warnings are errors. Common clippy groups to enable: `clippy::pedantic` (style), `clippy::correctness` (bugs). Suppress individual lints with `#[allow(clippy::...)]` only with a comment explaining why.

Format with `rustfmt`. Commit a `rustfmt.toml` with your project preferences. Run `cargo fmt --check` in CI — never let formatting drift.

## no_std Compatibility

If this crate should work in no_std environments (embedded, WASM without std), add the `no_std` gate at the top of `lib.rs`:

```rust
#![cfg_attr(not(feature = "std"), no_std)]

// Provide alloc types when std is not available
#[cfg(not(feature = "std"))]
extern crate alloc;
#[cfg(not(feature = "std"))]
use alloc::{string::String, vec::Vec, boxed::Box};
```

In `Cargo.toml`, add the `std` feature and make it the default:

```toml
[features]
default = ["std"]
std = []
```

Test both configurations in CI:
```bash
cargo test                              # with std
cargo test --no-default-features        # without std
```

Avoid `std`-only types (`HashMap`, `Mutex`, `Instant`, `thread`) in code guarded by `cfg(not(feature = "std"))`. Use `hashbrown` for `HashMap`, `spin` for mutexes, and `critical-section` for safe atomic access in embedded contexts.

Do not add `no_std` support unless there is a concrete use case — it adds meaningful maintenance burden.

## Common Pitfalls

- **Exposing internal types in public API.** If a `pub fn` returns a type from a `pub(crate)` or private module, users cannot name that type. Every type in the public API signature must be reachable by users.
- **Panicking in library code.** Never `unwrap()` or `expect()` on paths that depend on caller input. Return `Result` or `Option`. Reserve `expect()` for invariants that are truly programmer errors (not user errors).
- **API stability.** Every `pub` item is a stability promise. Mark experimental items with `#[doc(hidden)]` or put them behind a `unstable` feature flag. Removing a `pub` item is a breaking change.
- **Serde feature gating.** If serde is optional, gate it: `#[cfg_attr(feature = "serde", derive(Serialize, Deserialize))]`. Do not force downstream users to pull serde if they do not need serialization.
- **Linking issues with C dependencies.** If your crate links a C library via `build.rs`, document the system dependency in the README and add a CI step that verifies the link on a clean Ubuntu image.

## Performance Notes

Mark hot paths with `#[inline]` to encourage inlining across crate boundaries. Do not mark everything inline — the compiler inlines aggressively within a crate already.

Use `cargo bench` with Criterion for performance-sensitive code. Set a baseline before optimising and measure the delta. Never publish a performance claim without a reproducible benchmark.

For allocator-sensitive code, profile with `dhat` (heap profiling) before switching allocators. The default global allocator is correct for most use cases.
