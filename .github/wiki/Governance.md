# Governance

Cascade is an open-source project maintained by [Aric Camarata](https://github.com/ariccamarata) under the `acamarata` GitHub organization.

---

## Project status

Cascade is in active development. It is a FOSS portfolio project. The current phase (P4) covers the RAG pipeline, MCP server, and plugin system. See [Roadmap](Roadmap.md) for planned work.

---

## Maintainer

Aric Camarata (`@ariccamarata`) is the sole maintainer in the current phase. Responsibilities:

- Reviewing and merging pull requests
- Releasing new versions
- Responding to security reports (see [Security](Security.md))
- Setting project direction

---

## Decision making

Architectural decisions are documented as ADRs (Architecture Decision Records) in the `.claude/phases/` directory. For significant changes:

1. Open a GitHub issue describing the problem and the proposed solution.
2. If the maintainer agrees the approach is sound, the discussion moves to a PR.
3. Large or breaking changes may wait for a planned phase to coordinate with other work.

There is no formal RFC process at this project scale. Issue discussion is sufficient.

---

## Code of conduct

This project follows the [Contributor Covenant](https://www.contributor-covenant.org/) Code of Conduct. Report violations to the email address in `SECURITY.md`.

---

## Licensing

Cascade is MIT-licensed. All contributions must be compatible with MIT. See `LICENSE` at the repo root.

---

## Maintainer succession

If the project grows to the point where additional maintainers are needed, candidates will be nominated from consistent contributors. There is no formal process yet. Any nomination will be announced in a GitHub Discussion before taking effect.

---

## Versioning policy

Cascade follows Semantic Versioning (semver). In brief:

- **Patch** (`0.1.x`): bug fixes, documentation, internal refactors with no behavior change. These ship as soon as they are ready.
- **Minor** (`0.x.0`): new features, new subcommands, new configuration keys. These align with phase milestones (P4, P5, ...).
- **Major** (`x.0.0`): breaking changes to the CLI interface, config file format, or plugin ABI. Breaking changes require a migration guide and a deprecation window of at least one minor release.

The daemon IPC protocol (Unix socket JSON-RPC) is internal and not subject to semver guarantees. Plugin ABI stability starts at v1.0.0.

---

## Release process

Releases are tagged on `main` and published to all distribution channels by CI. The workflow:

1. CI passes on `main` (all tests, clippy, fmt).
2. A version bump commit is pushed (`chore: bump to vX.Y.Z`).
3. A git tag is pushed (`git tag vX.Y.Z`).
4. The release workflow builds binaries for all targets, publishes to crates.io, and creates a GitHub release with checksums.
5. Homebrew, AUR, Winget, Chocolatey, Scoop, Snap, and Flatpak packages are updated after the GitHub release is live.

---

See also: [Contributing](Contributing.md) · [Roadmap](Roadmap.md) · [Security](Security.md) · [Code Signing](Code-Signing.md)
