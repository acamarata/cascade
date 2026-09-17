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

## [v2.0.0-alpha.4]

Fourth wave-gate snapshot tag. Local build only: not published, not
installable, exists so Wave 4 could be checked as an artifact rather than
as a working tree. It is the **first** gate that can assert the full
install → `cascade init` → `cascade doctor` story, because `init` itself
did not exist before this wave.

### Added

- **`cascade init`**, the nine-step setup wizard: preflight, profile,
  storage, plugin catalog, providers, harness wiring, telemetry, daemon
  install and a first-run health check, with a journal so an interrupted
  run resumes rather than starting over. `--yes` answers every default,
  `--check` reports what a run would change without changing anything,
  `--config` reads a `cascade.init/v1` setup file, and `--reconverge`
  converges an already-configured machine while leaving the operator's own
  edits in place.
- **`cascade sync`**: `status`, `run`, `conflicts list` and
  `conflicts resolve`, over seven registered domains with their merge
  strategies, per-peer-tier eligibility and cursor positions. Discarding
  the server's copy of a record is the one elevated verb, and a caller
  with no elevation gate wired is refused rather than allowed through.
- **`cascade plugin`** now tells the truth about builtins: `list` carries
  a SOURCE column, `info` answers for a compiled-in plugin from the
  registry, and `enable`/`disable`/`remove` refuse one by explaining that
  it is always present and always active.
- **`cascade backup`** target management (`target add`/`list`), snapshot
  listing, and the recovery-key ceremony that `create`/`verify` require.
- **Node re-queue and tunnel state**: a lost node's work is re-queued, the
  resume point reaches the machine that acts on it, and the controller
  answers the tunnel question from the heartbeat it actually holds.

### Fixed

- `cascade init --check` could never exit 0: it counted a health check
  that changes nothing as a pending change. It now reports "nothing to do"
  on a converged machine, and plans creating the cascade home and the
  local database — the two largest changes a run makes, which it had never
  counted at all.
- `cascade init` reported `~/.cascade/cascade.db` as this machine's
  storage and created an empty database there, while everything else in
  the product opens `~/.cascade/data/cascade.db`.
- `cascade init` asked `Enable <plugin>?` for every builtin and
  discarded the answer; `--enable-plugin`/`--disable-plugin` printed a
  plan and performed none of it.
- `cascade doctor` reported the retrieval generation marker as drifted —
  and exited 5 — on any working tree with an uncommitted change, while
  `cascade recall index verify` reported the same marker current.
- Thirty CLI results printed Go's default formatting at operators, among
  them `cascade backup list` (`{[]}`) and `cascade backup target list`
  (`map[targets:[]]`).
- Every `cascade sync` verb leaked its database handle.
- The generated Homebrew cask failed `brew style`.

The full gate record, including the two residuals that need a second
machine or an owner-executed ceremony, is in `docs/waves/w4-gate-report.md`.

## [v2.0.0-alpha.3]

Third wave-gate snapshot tag. Local build only, same terms as the others.
Its entry is recorded here late: the W-3 gate landed its report and not
its changelog section, and the omission was found by the W-4 gate reading
back over this file.

Seven defects were found against the artifact and fixed in that wave,
including `cascade run` and the whole `cascade pbd` namespace never being
mounted, a security pipeline that was never wired, and an empty quota
spill order that vetoed every dispatch. Five P2s and one P1 were carried
into W-4 triage; §8 of `docs/waves/w4-gate-report.md` records what became
of each. The full report is `docs/waves/w3-gate-report.md`.

## [v2.0.0-alpha.2]

Second wave-gate snapshot tag. Local build only: not published, not
installable, exists so Wave 2 could be checked as an artifact rather than
as a working tree. Adds context/recall/memory/vault surface verification
on top of the alpha.1 W1 conditions. Two P1 defects and three P2 defects
were found and filed against the artifact during this gate (`cascade
recall` fails unconditionally on a fresh install; `cascade context slice`
does not bootstrap its own data directory on a virgin `$HOME`); see
`docs/waves/w2-gate-report.md` and `docs/releases/alpha2-dogfood.md` for
the full report and dogfood session this section is curated from.

## [v2.0.0-alpha.1]

First wave-gate snapshot tag. Local build only: not published, not
installable, exists so Wave 1 could be checked as an artifact rather than
as a working tree. See `docs/releases/alpha1.md` for the full snapshot
report this section is curated from.
