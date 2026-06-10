---
id = "cli-tool"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["cli-tool"]
description = "A command-line interface tool distributed as a binary or script, invoked by users in a terminal."
---

# CLI Tool

A program that users run from the terminal. The primary interface is the command
line: flags, subcommands, stdin/stdout. Installability and consistent behavior
across platforms are first-class concerns.

## Project Structure Expectations

Single repo. Entry point clearly named (`src/main.rs`, `src/cli.ts`, `src/index.ts`).
Subcommands modularized under `src/commands/` or equivalent.

Help text, flag definitions, and argument parsing centralized (don't scatter
`--help` strings across the codebase). Shell completion scripts generated from
the argument parser where the framework supports it.

Binary release artifacts (pre-compiled, not requiring a runtime) are the default
distribution for end-user tools. Script-based distribution is acceptable for
developer tools where the target environment has the runtime installed.

## Decision Norms

Breaking changes to the CLI interface (removed flags, changed subcommand names,
changed output format) follow the same semver rules as library APIs. Users script
CLI tools; breakage has real downstream cost.

Output format changes that affect parseable output (JSON, TSV) are breaking.
Changes to human-readable terminal output (spacing, colors, wording) are not.

## Code Review Conventions

Every command has tests that exercise its full argument surface against a mock or
test environment. Review flags that error flag: does the tool exit with a non-zero
status on every failure mode? Are error messages sent to stderr?

For interactive prompts: test with piped stdin (non-interactive) as well as terminal
input to catch scripts that break when run without a TTY.

## Release Cadence

Binary releases via CI on every tagged version. Platforms: at minimum the
developer's primary OS plus Linux amd64/arm64. Homebrew formula, AUR PKGBUILD,
or equivalent package manager integration reduces installation friction.

CHANGELOG entries describe the user-visible change, not the internal diff.
Write for the person who reads the release notes before upgrading.

## Documentation Expectations

`README.md` includes: install instructions for all supported platforms, a
quickstart showing the most common usage, and a full reference for all subcommands
and flags.

`man` pages or equivalent long-form docs are optional but valuable for complex tools.

Every subcommand has accurate `--help` text built into the binary.

## Dependency Philosophy

CLI tools benefit from a small dependency footprint. A tool that installs in
seconds and has no conflicting transitive deps wins adoption over an equivalent
tool that pulls in half of npm.

For Rust: evaluate whether `clap` or a lighter alternative fits. For Node.js:
prefer a single bundled binary (via `pkg`, `ncc`, or `esbuild`) over requiring
users to have Node installed. Avoid shipping `node_modules` to end users.
