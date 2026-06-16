---
id = "standard-rust"
version = "1.0.0"
tier = "any"
stacks = ["rust"]
project_shapes = []
description = "Rust coding standard: clippy -D warnings, rustfmt, Result error types, no unwrap in lib code, doc comments, tests."
---

# Rust Coding Standard

## Linting and Formatting

- `cargo clippy -- -D warnings` must exit 0 on every commit. No `#[allow]`
  suppressions without a comment explaining why.
- `cargo fmt --check` enforced in CI. Format on save in the editor.
- Enable additional lints in `lib.rs` / `main.rs`:
  ```rust
  #![warn(missing_docs, rust_2018_idioms, unreachable_pub)]
  ```

## Error Handling

- Library code (`src/lib.rs` and all modules it exposes): never call `.unwrap()`
  or `.expect()` on `Result` or `Option`. Return `Result<T, E>` with a typed
  error.
- Define crate-level error types with `thiserror` (preferred) or a hand-rolled
  enum. Implement `std::error::Error`.
- Binary entry points (`fn main`) may use `?` propagation; still avoid panics
  in hot paths.
- Use `?` for propagation; `map_err` / `and_then` for transformation. Avoid
  `.unwrap_or_else(|_| unreachable!())` patterns.

## Code Structure

- Files: ≤300 lines. Modules: one concern. Split with `mod foo;` into subfiles.
- Prefer `impl Trait` over generics when only one concrete type is expected at
  call sites.
- Mark items `pub(crate)` unless they belong to the public API surface. Keep the
  public API small and stable.

## Tests

- Unit tests in the same file as the code under test (inside `#[cfg(test)]` mod).
- Integration tests in `tests/`. Doc-tests for every public function example.
- `cargo test --all-features` must pass before any merge.
- Coverage target (library crates): statements ≥80%, functions ≥85%.

## Documentation

- `///` doc comment on every `pub` item: one-line summary, then elaboration,
  then an `# Examples` section with a runnable snippet.
- `cargo doc --no-deps` must build without warnings.
- Keep `README.md` in sync with the crate's public API surface after every
  breaking change.
