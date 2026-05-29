# Changelog

All notable changes to cascade are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)

## [Unreleased]

## [0.1.0] - 2026-05-28

### Added

- Gemini proxy daemon (`src/bin/cascade-gemini-proxy`) running on `localhost:3761`, rotating across 28 Gemini API keys from vault, writing per-account utilization to `~/.claude/temp/quota-state.json`
- Fleet dashboard web UI (`src/web/`) on `localhost:9761`, reading `quota-state.json` and rendering per-account utilization
- `install.sh` and `uninstall.sh` for local setup
- Absorbed claw-fleet (Gemini proxy daemon) and claw-dash (dashboard web UI) into a single unified tool
- MIT license

[Unreleased]: https://github.com/acamarata/cascade/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/acamarata/cascade/releases/tag/v0.1.0
