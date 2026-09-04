# Wave 1 alpha

This is a local snapshot build of the first wave of Cascade v2. It is not published, not
tagged on the public repository, and not something to install from. It exists so the wave
can be checked as an artifact rather than as a working tree.

## What works

One binary, `cascade`, which is both the CLI and, through `cascade daemon run`, the
long-lived daemon.

- **`cascade daemon`**: run, start, stop, restart, status. Starting it creates a unix
  socket under the profile's runtime path, opens the on-disk SQLite store, converges the
  schema through the real migrator, and runs a crash-recovery scan that clears a stale
  pidfile, a dead socket and orphaned advisory locks left by a previous process.
- **`cascade status`**, including `--json`, which returns a versioned envelope.
- **`cascade config`**: read, set, edit, with the write path refusing to persist a value
  that looks like a credential.
- **`cascade mcp`**: an MCP server over stdio or a socket, also mounted on the daemon's
  own socket.
- **`cascade vault`** and **`cascade elevate-helper`** (hidden), the elevation and secret
  custody surface.
- **IPC**: JSON-RPC over the unix socket, plus a server-sent event stream at `/events`
  with replay cursors that survive a restart.
- **Storage**: eleven domains, per-domain export and import, retention and vacuum jobs on a
  persisted cron scheduler with an advisory lock, and a typed event bus.

## What does not work

Read this part. The list is short and each item is deliberate.

**Elevation is unavailable in release binaries.** Every artifact is built with cgo
disabled, and both hardware-backed keystores sit behind cgo: the macOS keychain path is
`darwin && cgo`, the Linux PAM path is behind its own build tag. What ships is the fallback
pair, and both refuse rather than degrade: `IsAvailable` reports false, and key generation
and signing return an unavailable error. In practice no user of a release binary can enroll
a key or sign an attestation, so no elevated verb can be authorized at all. Nothing weak
ships and nobody is misled into thinking they have local auth, but the feature is not there.
Whether to ship cgo-enabled artifacts, a pure-Go keystore with an honestly weaker guarantee,
or elevation explicitly disabled, is an open release-engineering decision.

**The signing key is not hardware-bound.** Where elevation is available at all (a
cgo-enabled build on macOS), the Ed25519 key is generated in software and stored in the
keychain behind an access control requiring device-owner presence. Apple's Secure Enclave
generates only P-256 keys and cannot hold an Ed25519 key, so a hardware-held signing key is
not available from Apple's public APIs. The operating system gates access and approval needs
your presence; the key material still exists in process memory when it is created and each
time it signs. `docs/elevation.md` says the same thing in more detail.

**`cascade doctor` does not exist yet.** The diagnostic engine is built and tested, but no
command mounts it, so there is nothing to run. Documentation that refers to it is ahead of
the binary.

**The MCP wire fixtures are self-authored.** The golden request and response bytes this
build's MCP tests assert against were written from the protocol specification rather than
captured from a real client. They prove the implementation agrees with itself and would not
catch a divergence between the specification text and a real client's bytes. This is
recorded in `internal/mcp/testdata/README.md` rather than glossed over.

**`cascade daemon restart` reports a timeout under load.** Stop waits for the socket file
to disappear rather than for the old process to exit, and the daemon unlinks its socket
before it has closed the database, checkpointed the write-ahead log and released its
advisory lock. The replacement is therefore spawned while the old one still owns the store,
and the new daemon's readiness budget of roughly seven seconds can expire before it binds.
The restart usually completes anyway, a moment after the command has already reported
failure. Measured on a moderately loaded development machine, the end-to-end restart check
failed about a third of the time.

**Windows is tier 2.** The binary builds and one-shot commands work; the daemon and every
socket-backed surface refuse explicitly rather than half-working.

**No retention policy is configured.** The prune and vacuum jobs run on schedule, but no
retention window is set for any domain, so the event log is not yet trimmed.

**No retention policy is configured** (see above), and until this build the scheduled prune
job could not have run even if one had been: it was registered without a clock and returned
an error on every fire. That is fixed here; choosing the retention windows is still open.

## Platforms

darwin arm64 and amd64, linux amd64 and arm64, windows amd64. Every artifact is a static
cgo-free binary. The snapshot produces a tar.gz per unix platform, a zip for windows, an
SBOM alongside each, and a checksums file signed with minisign; the signature was verified
against the matching public key as part of this build.

## Verification status

This alpha was built and exercised on one macOS development machine. It was not installed
or run on a clean macOS machine, and not run on Linux at all, so the cross-platform claims
above rest on the cross-compile and the CI matrix rather than on a hands-on check. Treat the
Linux and Windows behaviour as unverified for this build.
