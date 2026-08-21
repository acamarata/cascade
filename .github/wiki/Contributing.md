# Contributing

Cascade is MIT-licensed and open to contributions. This page covers the contribution workflow. Full developer setup is on the [Development Setup](Development-Setup.md) page. Test instructions are on the [Testing](Testing.md) page.

---

## Before you start

1. Check [open issues](https://github.com/acamarata/cascade/issues) and [open PRs](https://github.com/acamarata/cascade/pulls) to avoid duplicate work.
2. For anything beyond a small bug fix, open an issue first to discuss the approach. Large PRs without prior discussion may be declined.
3. Read [CONTRIBUTING.md](https://github.com/acamarata/cascade/blob/main/.github/CONTRIBUTING.md) in the repo. This wiki page is a summary; the repo file is the canonical source.

---

## Development setup

See [Development Setup](Development-Setup.md) for the full Rust toolchain, pnpm, and Tauri prerequisites.

Short version:

```bash
git clone https://github.com/acamarata/cascade.git
cd cascade
cargo build
cargo test
```

---

## Contribution workflow

1. Fork the repo on GitHub.
2. Create a branch: `git checkout -b fix/my-bug` or `feat/my-feature`.
3. Make your changes. Follow the conventions below.
4. Run the tests: `cargo test`.
5. Check formatting: `cargo fmt --check`.
6. Check lints: `cargo clippy -- -D warnings`.
7. Push to your fork and open a pull request against `main`.

---

## What we accept

- Bug fixes with a clear reproduction and a test
- Performance improvements with benchmark data
- Documentation fixes and improvements
- New CLI subcommand ideas discussed in an issue first
- New plugin templates (see [Plugin Development Guide](Plugin-Development.md))
- New bundled templates (see [Templates](templates.md))

---

## Code conventions

- Rust: follow the existing style. `cargo fmt` handles formatting. `cargo clippy` must pass.
- No file over 300 lines. Split by domain if a file grows.
- Every public function gets a doc comment explaining what it does and why it exists.
- Tests live in the same file as the code they test, in a `#[cfg(test)]` module.
- Integration tests live in `crates/<crate>/tests/`.

---

## Commit style

Use conventional commits:

```
feat(rag): add configurable chunk overlap
fix(daemon): handle Unix socket path with spaces
docs(wiki): add Configuration page
```

Prefix options: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `perf`.

Keep the subject line under 72 characters. Add a body if the change needs explanation.

---

## Pull request checklist

Before requesting review:

- [ ] All tests pass (`cargo test`)
- [ ] No clippy warnings (`cargo clippy -- -D warnings`)
- [ ] Code is formatted (`cargo fmt`)
- [ ] Relevant docs updated (this wiki, CLI reference, or inline docs)
- [ ] New behavior has tests
- [ ] Breaking changes are documented in the PR description

---

## Reporting bugs

Open a [GitHub issue](https://github.com/acamarata/cascade/issues/new?template=bug_report.md). Include:

- Cascade version: `cascade --version`
- Operating system and version
- Steps to reproduce
- What you expected vs what happened
- Relevant log output: `cascade status`, `cascade doctor`, or daemon logs

---

## Security vulnerabilities

Do not open a public issue for security vulnerabilities. Follow the process in [SECURITY.md](https://github.com/acamarata/cascade/blob/main/.github/SECURITY.md). See the [Security](Security.md) wiki page for the threat model.

---

## License

By contributing, you agree that your contributions will be licensed under the MIT license, consistent with the rest of the project.

See also: [Development Setup](Development-Setup.md) · [Testing](Testing.md) · [Building From Source](Building-From-Source.md)
