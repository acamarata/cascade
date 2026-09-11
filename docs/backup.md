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

## Scope

This document covers the engine `internal/backup` ships: the repository
layout, the capture adapters, and the chunk → dedup → compress → encrypt
pipeline (including its own read-side verification). Not covered here,
because they are not built yet:

- **Targets** — concrete fs/S3/rclone `Target` implementations.
- **Snapshots and manifests** — the signed record of which object ids
  make up one point-in-time backup.
- **Restore** — domain selection and the integrity gate over a whole
  snapshot (distinct from the pipeline's own per-chunk verification
  above).
- **The recovery-key ceremony** — issuing, wrapping, and escrowing the
  age identity this pipeline consumes.
- **The `backup` CLI/MCP surface.**
