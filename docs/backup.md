# Backups

`internal/backup` is the engine underneath a future `backup` command
surface: capture, chunk, deduplicate, compress, and encrypt one domain at
a time into a target-agnostic repository. This document covers what
`internal/backup` ships today. The command surface, snapshot manifests,
storage targets, and restore orchestration are separate, later pieces —
see Scope below.

## Repository format

A backup repository is a flat key space any `Target` (an object put/get/
list/delete abstraction) can serve — a local directory, S3-compatible
storage, or an `rclone` remote. The engine never assumes a filesystem;
every path below is a **key**, not a directory that must exist ahead of
time.

```
config/repo.json      the repo's layout version + its age recipient (public)
manifests/             snapshot manifests land here (a later piece)
objects/<xx>/<hash>    one stored chunk, sharded by the first two hex
                        characters of its content hash
```

Everything under `objects/` and `manifests/` is opaque ciphertext to
anyone without the repo's age identity. `config/repo.json` is the one
document a target operator can read: it carries the repo's layout version
and its **age recipient** — a public key. It never carries a private
identity.

## Capture

Capture rule (binding, §D-15): a SQLite-backed domain is captured through
a consistent snapshot, never a raw copy of the live WAL file. This build's
SQLite driver is `modernc.org/sqlite` — pure Go, no CGO — which has no
binding for SQLite's C-level `sqlite3_backup_*` online-backup API,
so the engine uses SQLite's `VACUUM INTO` instead: a `VACUUM INTO` runs
inside SQLite's own deferred read transaction, so a writer holding an
uncommitted transaction on the source database is never visible to the
capture, regardless of timing. The temp file `VACUUM INTO` produces is
read once (via the per-domain export path) and removed immediately,
success or failure — a capture never leaves a stray copy of the database
behind.

Every domain this build captures is SQLite-backed (the eleven
`internal/storage` domains). The contract also names a second family, a
non-SQLite domain captured through "the B/S-03.T3 export path" — no such
domain exists in the tree yet, and `internal/storage`'s per-domain export
(`storage.Export`) only has a SQL-backed implementation today. The engine
expresses this as an `Exporter` interface rather than a concrete type, so
a future non-SQLite domain plugs in without touching the pipeline.

A capture that fails for any reason — a closed database, a `VACUUM INTO`
error — returns a typed refusal and never ships a partial snapshot.

## Pipeline

Four stages, in this fixed order:

1. **Chunk** — content-defined chunking (a polynomial rolling hash over
   the capture stream) splits it into variable-size chunks. Cutting on
   content, not fixed offsets, means an edit anywhere in the stream
   re-cuts only the chunks touching it; everything else re-hashes
   identically across snapshots.
2. **Dedup** — each chunk's object id is the BLAKE3 hash of its
   **plaintext**, computed before compression or encryption. Hashing the
   plaintext (not the eventual stored bytes) is what makes dedup possible
   at all: age's encryption is randomized, so the same plaintext chunk
   produces different ciphertext on every run, and hashing ciphertext
   would defeat dedup entirely. A chunk whose object id is already
   present in the target is skipped — "only new/changed chunks upload."
3. **Compress** — zstd, pure Go (`klauspost/compress`), no CGO.
4. **Encrypt** — age, the reference implementation (`filippo.io/age`),
   never a hand-rolled cipher. Compress runs **before** Encrypt, not
   after: compressing a payload that is already indistinguishable from
   random bytes finds nothing to remove. This ordering is a documented
   tradeoff, not a free lunch — compressing before encryption leaks the
   plaintext's approximate length through the ciphertext's length. The
   alternative (encrypt then compress) would hide that leak at the cost
   of every chunk shipping uncompressed.

Reading reverses the last three stages per chunk (fetch, decrypt,
decompress) and re-hashes the recovered plaintext against its recorded
object id before accepting it — a chunk that decrypts and decompresses
cleanly but does not match its recorded hash is still refused. This
reverse path is the pipeline's own verification half; restore
orchestration (domain selection, an integrity gate over a whole snapshot)
is a separate, later piece.

### Integrity

A corrupted, truncated, or tampered stored chunk is refused, never
partially returned. age's STREAM construction authenticates every block;
when authentication fails, no plaintext byte reaches the caller — the
engine discards whatever bytes were buffered mid-read rather than handing
back a partial result.

### Key custody

The pipeline consumes key material; it does not generate, wrap, or
escrow it. A recipient string encrypts; the corresponding identity string
decrypts. Missing key material fails closed: the pipeline never writes an
unencrypted chunk. No private or secret key material is ever written into
the repository layout, a log line, or an error message — `config/
repo.json` carries the public recipient only. The ceremony that issues
and custodies the identity, and the `backup key export|import` CLI
surface, are separate pieces.

## Restore & integrity gate

`VerifyIntegrity` (`integrity.go`) is the fail-closed engine behind
`backup verify` and the verification cron: given a snapshot id, it walks
the manifest's `previous_snapshot` chain back to genesis, re-verifying
every ancestor's Ed25519 signature, blake3 root_hash, and chain link (via
`ReadManifest`), then re-decrypts and hash-checks every object the target
snapshot's manifest lists (reusing the pipeline's own per-chunk
verification, so the same call proves object presence and AEAD-tag
authenticity together). A tampered signature, a root_hash mismatch
anywhere in the history, a broken or cyclic chain link, a truncated or
bit-flipped object, or a missing object all refuse before returning —
there is no partial-trust result and no flag that skips a check.

`Restore` (`restore.go`) is the elevated `backup restore` operation: it
runs the gate FIRST, refusing without touching the destination on any
gate failure, then resolves the effective domain set (bounded by the
manifest's own domain list — `restore --domain` never restores a domain
the snapshot never captured), fetches and reassembles each domain's
export stream through the pipeline's read path, and applies it via
`storage.Import`. No domain's `storage.Import` call runs until the gate
has already passed for the whole snapshot, so a refused restore leaves
the destination database exactly as it was found — never a half-applied
recovery. `backup restore` carries the same elevated-verb boundary
`backup create` does: a non-empty `ElevationProof` is required before any
work starts; the real CLI/MCP attestation flow (and the Windows tier-2
refusal that rides it) is the CLI/MCP surface's concern, not this
engine's.

Key custody for both: the backup age identity is resolved from
`CASCADE_BACKUP_AGE_IDENTITY` (vault/env-ref only, per the pipeline's own
key-custody rule above) — never caller-supplied, never persisted beside
the target. An unset or malformed reference refuses before any Target
call.

## Targets

`internal/backup/targets` implements `Target` (`repo.go`'s put/get/list/
delete abstraction) three ways, each with a different integration mode:

- **fs** (`FSTarget`) — a local filesystem root. Every write is
  temp-file-then-rename so a crash mid-write never leaves a partial
  object visible to a concurrent read. The default target.
- **s3** (`S3Target`) — the real S3 REST API over `minio-go/v7`. This is
  a standalone client this package constructs and owns: it does **not**
  import `providers/s3` (the server profile's BlobStore driver) or its
  wiring, even though both packages depend on the same wire library.
  Credentials (endpoint, bucket, access key id, secret access key)
  resolve through `ResolveS3TargetEnvRefs`, an `s3_env_prefix`-expanded
  family of four env-refs (`_ENDPOINT`/`_BUCKET`/`_KEY_ID`/`_SECRET`,
  the same shape 08-INIT-CONFIG-SPEC §2 defines) — never a literal
  credential.
- **rclone** (`RcloneTarget`) — exec-only against the real `rclone`
  binary (`rcat`/`cat`/`lsjson --recursive`/`deletefile`), no rclone
  Go-library linkage. This is the one target covering the NAS/B2/WebDAV/
  Google Drive universe behind a single external tool. An absent binary
  is a typed refusal at first use (`ErrRcloneBinaryAbsent`), never a
  silent skip.

Both remote targets (s3, rclone) route every outbound payload through
the `backup-target` egress class (`egress.go`): each holds an unforgeable
`egress.Capability` and calls `Intercept` with the tier declared
explicitly before a byte reaches the wire or the subprocess. `fs` never
does this — a local filesystem write is not egress.

### The rclone version probe (`cascade doctor`)

`RcloneDoctorCheck` (`doctor.go`) probes `rclone version` and reports it
under the check name `backup`, registered at `cmd/cascade/doctor_mounts.go`'s
composition root. It runs as part of every plain `cascade doctor`
invocation — there is no `--backup` flag, matching the same gap the
`nodes` check's own doc comment already records for `--nodes`.

### Error taxonomy

Every target maps its own failure shapes onto the frozen 14-kind
taxonomy: an unreachable endpoint or remote is `KindUnavailable` or
`KindTimeout`; a missing object is `KindNotFound`; an auth failure is
`KindPermissionDenied`; an absent `rclone` binary is `KindUnsupported`;
a missing or unset env-ref is `KindInvalidInput`. No target ever falls
back to a different target on failure.

## Scheduling & multi-target policy

`internal/backup`'s `TargetRecord`/`TargetPolicy`/`Outcome` types
(`targets.go`, `policy.go`, `schedule.go`) are the persisted registry, the
per-target cadence policy, and the scheduler integration every target
rides on — the data model behind the (not-yet-built) `backup target
add|list|remove` CLI verbs.

- **Target registry** — a `TargetRecord` names a target (name, kind
  fs|s3|rclone, driver config) persisted via `provider.Store`
  (`PutTarget`/`GetTarget`/`DeleteTarget`/`ListTargets`, key prefix
  `backup:target:`). Driver config is never a literal credential: `fs`
  stores a local root path, `s3` stores only the `s3_env_prefix` name
  `ResolveS3TargetEnvRefs` expands (never the resolved secret), and
  `rclone` stores the remote spec (rclone resolves its own credentials
  from the operator's own rclone config, outside this record). Every
  kind-specific field is checked against the H/S-15.T3 detector at its
  default threshold before it is ever persisted — a secret-shaped
  literal is refused at registration time.
- **Per-target cadence policy** — a `TargetPolicy` (key prefix
  `backup:policy:`) names one target's own cron spec (parsed through
  C/S-04.T4's `scheduler.ParseSpec` — no new parser) and domain set. One
  target, one policy, one scheduled job: a due fire always selects
  exactly that policy's target and domain set by construction, never a
  selection step that could pick the wrong one.
- **Scheduler semantics, inherited verbatim** —
  `RegisterConfiguredBackupJobs` (the daemon composition root's entry
  point, called from `cmd/cascade/daemon_unix_scheduler.go`'s
  `startScheduler` alongside the retention and memory jobs) registers one
  `scheduler.CronJob` per configured target on C/S-04.T4's persisted
  cron scheduler: job persistence across restart, the advisory lock
  excluding a second scheduler handle, and skip-missed (a long outage
  fires once for the next valid window, never once per missed window)
  are the scheduler's own unmodified behavior — this package adds no new
  scheduling logic of its own.
- **ActionRouter gating** — every dispatch on that scheduler, backup jobs
  included, already transits `Scheduler.routeDispatch`
  (I/S-18.T5's `ActionGate`, installed once by the composition root) with
  a routing-decision audit event. There is no separate or bypassable
  gate for backup jobs specifically.
- **No elevation bypass** — past that gate, the registered Runnable calls
  `CreateSnapshot` with a **hardcoded empty `ElevationProof`**: an
  unattended cron tick has no interactive attestation to supply, and
  06 §5.24 forbids a standing grant for an elevation-class verb.
  `CreateSnapshot`'s own `ErrElevationRequired` refusal — its very first
  check, before the domain set or signing key are ever touched — is what
  makes "no snapshot, ever, from an unattended fire" true by
  construction. An authorized (non-empty-proof) fire runs the real
  stack end to end; only the unattended scheduled path is permanently
  refused until a future ticket supplies a real attended attestation
  flow.
- **Per-target outcome records** — every fire, successful or refused,
  writes an `Outcome` (target, snapshot id if any, when, success,
  error text) via `RecordOutcome` (key prefix `backup:outcome:`,
  chronologically ordered per target via `ListOutcomes`) — the
  bookkeeping the (not-yet-built) verification cron and lose-the-laptop
  drill read.

## Scope

This document covers the engine `internal/backup` ships: the repository
layout, the capture adapters, the chunk → dedup → compress → encrypt
pipeline (including its own read-side verification), the fs/s3/rclone
targets, and restore + the integrity gate. Not covered here, because they
are not built yet:

- **Snapshots and manifests** — the signed record of which object ids
  make up one point-in-time backup.
- **The recovery-key ceremony** — issuing, wrapping, and escrowing the
  age identity this pipeline consumes.
- **The `backup` CLI/MCP surface.**
