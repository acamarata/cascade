# Contributing to Cascade

Thanks for your interest in improving Cascade. This guide covers the basics.

## Development setup

Prerequisites: Rust stable (1.85+), pnpm, and Node 20+.

```bash
git clone https://github.com/acamarata/cascade.git
cd cascade
cargo build --workspace
pnpm --dir apps/cascade-app install
pnpm --dir apps/cascade-dashboard install
```

## Running tests

```bash
cargo test --workspace                       # Rust suites
pnpm --dir apps/cascade-app exec vitest run  # Desktop app tests
pnpm --dir apps/cascade-dashboard exec vitest run
```

Run `cargo clippy --workspace --all-targets` and `cargo fmt --all --check`
before opening a PR. CI enforces both.

## Project layout

- `crates/` — Rust workspace (types, core, cli, daemon, rag, mcp, plugins,
  providers, local-llm, harness, pdk, keychain, audit, tray)
- `apps/cascade-app` — Tauri 2 desktop app (React + Vite + TypeScript)
- `apps/cascade-dashboard` — browser dashboard SPA
- `data/templates/` — bundled cascade templates
- `plugins/`, `examples/plugins/` — first-party and example WASM plugins

## Pull requests

1. Fork and create a feature branch.
2. Keep changes focused. One concern per PR.
3. Add or update tests for any behavior change.
4. Make sure the full test suite passes locally.
5. Sign off your commits (`git commit -s`) to certify the
   [Developer Certificate of Origin](https://developercertificate.org/).

## Reporting bugs and requesting features

Use the issue templates. For security issues, do NOT open a public issue;
see [SECURITY.md](../SECURITY.md).

## Plugin development

See the wiki page "Plugin Development" and run `cascade plugin new my-plugin`
to scaffold from the bundled template.

## License

By contributing, you agree that your contributions are licensed under the MIT
License.
