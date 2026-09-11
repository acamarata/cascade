# Changelog

All notable user-visible changes to this project are documented here, in
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format. Every
change that changes something a user or plugin/provider author can
observe lands an entry here, in the same change that makes it.

Wave-gate alpha tags cut during this phase's build (`v2.0.0-alpha.N`) are
snapshot checkpoints, not independent releases: each one's entries roll up
into the `v2.0.0` release section below rather than getting a permanent
section of their own once `v2.0.0` ships.

## [Unreleased]

Work in progress toward `v2.0.0`, the first release of this rewrite.
Nothing in this section has been tagged for release yet; see
`docs/developer/versioning/version-scheme.md` for what a release tag
means and `docs/developer/release.md` for the pipeline that cuts one.

### v2.0.0 surface, by area (curated from the wave-gate alpha entries)

- **Daemon**: `cascade daemon run/start/stop/restart/status`, a unix
  socket per profile runtime path, on-disk SQLite storage with the real
  schema migrator, and a crash-recovery scan that clears a stale pidfile,
  a dead socket, and orphaned advisory locks left by a previous process.
- **CLI**: `cascade status` (including `--json`, a versioned envelope),
  `cascade config` (read/set/edit, refusing to persist a value that looks
  like a credential), `cascade mcp` (an MCP server over stdio or a
  socket, also mounted on the daemon's own socket), and the hidden
  `cascade vault` / `cascade elevate-helper` elevation and secret-custody
  surface.
- **IPC**: JSON-RPC over the unix socket, plus a server-sent event stream
  with replay cursors that survive a restart.
- **Storage**: eleven domains, per-domain export/import, retention and
  vacuum jobs on a persisted cron scheduler with an advisory lock, and a
  typed event bus.
- **Install channels**: `script` is live for every artifact this pipeline
  currently produces; `brew`, `oci`, and `node-managed` are the remaining
  §D-33 channel values, landing as their respective tickets complete (see
  `docs/developer/release.md`).

### Known gaps at this checkpoint

Documented honestly rather than silently: elevation is unavailable in
release binaries (both hardware-backed keystores sit behind `cgo`, and
every release artifact ships `cgo`-disabled); the signing key is not
hardware-bound (Apple's Secure Enclave cannot hold the Ed25519 key type
this project signs with); `cascade doctor` is built and tested but not
yet mounted on any command; and the MCP wire fixtures are self-authored
against the protocol specification rather than captured from a real
client. Full detail: `docs/releases/alpha1.md`.

### v1 to v2 migration

A `cascade migrate v1` command is planned to carry v1 state into the v2
daemon's storage layout. It has not landed yet; this entry is a forward
pointer, not a claim that the command exists. It will get its own
changelog entry, under this same `v2.0.0` section, the day it ships.

## [v2.0.0-alpha.1]

First wave-gate snapshot tag. Local build only: not published, not
installable, exists so Wave 1 could be checked as an artifact rather than
as a working tree. See `docs/releases/alpha1.md` for the full snapshot
report this section is curated from.
