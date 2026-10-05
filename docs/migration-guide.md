# Migrating v1 data

Cascade v2 reads v1 data through four strict importers. The v1 archive is a
format reference only. No v1 implementation code runs inside v2.

Import one destination domain at a time:

```console
cascade migrate v1 memory --from /path/to/v1-home --dry-run
cascade migrate v1 vault --from /path/to/v1-home --dry-run
cascade migrate v1 accounts --from /path/to/v1-home --dry-run
cascade migrate v1 config --from /path/to/v1-home --dry-run
```

Remove `--dry-run` only after reviewing the reported delta. Each command
validates its complete input before writing. A malformed, unknown, conflicting,
or unsupported-version input refuses the import with a typed error. It never
imports a guessed subset.

## Source locations

| Domain | Recognized v1 source |
|---|---|
| Memory | `.cascade/memory/*.md` and `.md.tombstone` |
| Vault | `.claude/vault.env` or legacy `.cascade/vault.env` |
| Accounts | `.cascade/accounts/accounts.json` or legacy `.cascade/accounts.json` |
| Config | `.cascade/config.toml` |

If both canonical and legacy locations exist for one domain, the importer
refuses the ambiguity. Symlinks and non-regular source files are also refused.

## Memory

The memory importer accepts the plain `decisions.md`, `lessons.md`, and
`patterns.md` collections observed in the v1 archive, plus the observed strict
frontmatter form. It preserves source bytes, original modification time, kind,
source path, and a BLAKE3 content hash. The portable file stem becomes the v2
record identity; the v1 display name remains intact in the preserved source
bytes. Tombstones stay tombstones.

The complete source directory is parsed before the files-first v2 memory store
is touched. Destination data with different content or provenance causes a
conflict. A write failure restores the whole batch.

## Vault

The vault importer supports comments, `export KEY=value`, single or double
quotes, and quoted multiline values. Valid variable names without a special v2
meaning remain opaque secret names. Duplicate names use the last assignment and
produce a journal warning.

Credential bytes pass directly from the parser to the configured vault custody
backend in memory. No temporary credential file is created. Before applying a
batch, the importer snapshots affected values in memory. A failed write restores
all earlier values. A second identical run reports every key unchanged.

Committed vault fixtures contain only the literal `REDACTED` value. The importer
refuses that sentinel to prevent a fixture from becoming operational data.

## Accounts

Schema-version 1 account JSON is decoded with unknown-field rejection. The
translation maps the v1 family, role, authentication shape, known models, and
account identity into the provider registry. It sets health to unknown because
the v1 account record has no durable health reading. Credentials are not copied.
Every account returns a re-authentication prompt with its supported v1 access
methods.

Provider records are created in one database transaction. A provider with the
same translated name wins unchanged and is reported as `skip-existing`.
Additional known v1 metadata and the routing matrix are validated and recorded
by digest in the import journal so their disposition is explicit. An unknown
field, enum, route, or task class refuses the complete batch.

## Config

The verified v1 path is `.cascade/config.toml`. The current field mappings are:

| v1 key | v2 key | Notes |
|---|---|---|
| `schema_version` | `schema_version` | v1 versions 0 and 1 map to the current v2 version |
| `daemon.log_level` | `logging.level` | `debug`, `info`, `warn`, or `error` only |
| `daemon.log_format` | `logging.format` | `pretty` becomes `text`; `json` stays `json` |
| `daemon.socket_path` | `daemon.socket` | Non-empty strings only |
| `telemetry.enabled` | `telemetry.enabled` | Boolean only |

Every other leaf, including a newly introduced v1 key, is returned as a
`v1_unknown_config` journal entry and causes a fail-closed refusal. Nothing is
written until the candidate v2 document passes the established runtime config
validator. A different value already present at a mapped destination key is a
conflict, not an overwrite. The final update uses the runtime's atomic config
writer.

## Repeat runs and recovery

All four importers converge. Re-running identical input exits successfully with
an empty mutation delta. Existing-wins and unchanged rows remain visible in the
result but are not counted as mutations. Dry runs use the same parsers, store
lookups, conflict checks, and validation as live runs while performing zero
writes.

## Recall index rebuild and golden parity

After a successful non-dry `cascade migrate v1 --from <home> --yes`, the
command rebuilds the recall index over migrated memory and prints its verify
report after the import report. Rebuild or verification failure exits non-zero;
completed import ledger rows remain done. Re-running retries the rebuild while
completed domains are skipped. A dry run never rebuilds the index.

After memory content lands in its v2 destination, `internal/migration/v1`'s
`RebuildIndex` re-runs the same chunk/FTS5/vector index build the retrieval
system's own `recall index rebuild` verb uses (F/S-10 chunking, F/S-11 write
and verify), pointed at the migrated content's directories instead of the
retrieval config's normal registered sources. It calls no new ingest or
chunking logic of its own; it composes the existing chunkers and
`internal/retrieval/lifecycle.Manager` exactly as
`cmd/cascade/doctor_recall_index.go`'s `buildRecallIndexManager` already does,
then verifies the result the same way `cascade doctor` does. The rebuild is
idempotent: re-running it over an unchanged migrated tree writes no new
entries and deletes none.

`ParityChecker`, alongside it in the same package, is CI-gate infrastructure,
not a runtime tool: it runs a small set of golden recall queries — harvested
from a real, self-hosted v1 installation's own retrieval index, with
provenance recorded in
`internal/migration/v1/testdata/v1-goldens/recall/README.md` — against a
freshly rebuilt index, and requires exact top-k content-hash coverage. Any
accepted divergence is recorded in that directory's `divergence-ledger.yaml`
with the query, the missing or surplus hashes, and the rationale; an
unratified divergence fails the check rather than passing silently.

## Golden fixtures and the checksum tripwire

From a source checkout, harvest migration fixtures with
`go run -p 4 ./internal/migration/golden`. Review the resulting bytes and
provenance before refreshing `internal/migration/testdata/golden-checksums.sha256`.
The checksum set covers all files, including provenance READMEs, under the
four `testdata/v1-goldens/migration/` directories. Historical fixtures and
README rows retained by the harvester remain covered; they are not pruned
or silently ignored.

Run this checksum regeneration block from the module root after review:

```sh
python3 - <<'PY'
from pathlib import Path
from hashlib import sha256
roots = [Path('internal') / base / 'testdata/v1-goldens/migration'
         for base in ('memory', 'secrets', 'providers/registry', 'runtime')]
paths = []
for root in roots:
    assert root.is_dir() and not root.is_symlink(), root
    for path in root.rglob('*'):
        assert not path.is_symlink(), path
        if path.is_file():
            paths.append(path)
assert paths, 'empty golden fixture set'
header = '# Harvester format version: 1\n'
header += '# Generation command: python3 - (checksum regeneration block in docs/migration-guide.md)\n'
rows = [f'{sha256(p.read_bytes()).hexdigest()}  {p.as_posix()}\n'
        for p in sorted(paths)]
Path('internal/migration/testdata/golden-checksums.sha256').write_text(
    header + ''.join(rows), encoding='utf-8', newline='\n')
PY
```

`TestTripwireStable` runs without build tags and fails with the changed,
added or missing paths. An empty or missing checksum set also fails.
Paths use slash separators; checksums cover raw bytes, so renames and
line-ending changes count. Symlinks and roots outside the module are refused.

Inside this source module, `cascade migrate v1 --dry-run` checks before
the first importer; an actual import checks afterward. A stale checksum
reference produces one stderr warning naming the files and leaves the
migration result and exit code unchanged. Current checksums produce no
warning. Staleness means a checksum difference, not elapsed time. Outside
the source module, including a checkout of another module, no check runs.

## End-to-end migration check

From the module root, run:

```console
go test -p 4 -tags integration -count=1 -v ./internal/migration/ -run '^(TestEpicZMigration|TestEpicZMigration_RawRedactedRefuses)$'
```

The test builds the CLI once and materializes the committed dot-free
`internal/migration/testdata/v1-home` fixture under a temporary home. Its
README records the source files and checksums. Vault names are synthetic;
the successful derivative replaces redacted values with logged NONSECRET
placeholders only in the temporary copy. The raw fixture must be refused.

Before any CLI step, a read-only custody guard requires an unresolved default
keychain on macOS, or a disabled session bus with no runtime bus on Linux.
An unsupported or uncertain result fails before any step runs. HOME,
USERPROFILE, CASCADE_HOME and all XDG directories point into temporary paths.
The subsequent doctor custody check must report the file-vault backend.
Every doctor outcome must be `ok`, except fresh-home `warn` results for
`completion-gate-hooks`, `hook-events`, `provider_health` and `subsystem_census`.
The test accepts exit 0, or exit 5 with only those warnings. Any other outcome,
warning name or exit code fails the check.

The sequence checks dry-run counts, empty domain destinations and ledger,
then full import counts, completed ledger rows, exact vault contents and a
healthy recall rebuild. Config records source keys and planned changes
separately: the additional change is exactly `schema_version`. The account
absence check reads `provider_records` in the actual `providers.db` store;
initializing empty database schemas during dry run is allowed. It then runs
doctor, context sync with `--check` over a temporary fixture project, and the
golden checksum tripwire. Committed fixture hashes must remain unchanged.

## Legacy pointer sweep

A separate, local-only engineering tool — `internal/migration/sweep`, never a
shipped `cascade` subcommand — text-scans the tracked v2 tree for retired v1
CLI command names, config keys, and design-provenance markers, and classifies
each hit from the committed term file's own replacement declaration. It
exists to guarantee the lose-nothing invariant: no v1 dependency or behavior
is dropped without an explicit, verified v2 replacement. It reads no private
planning input — every fact it needs is either on the term itself or on disk
at a path the term names.

Run it with:

```console
go run ./internal/migration/sweep --files <(git ls-files -z) [--dry-run]
  [--root <dir>] [--terms <file>] [--provenance <file>] [--fail-on-unresolved]
```

`--files` is required: a NUL-separated list of tracked paths (`git ls-files
-z` output, `-` for stdin, or a process substitution), since the sweep never
walks the filesystem itself — only a file actually tracked by git can enter
the inventory. The walked set is filtered to `cmd`, `internal`, `pkg`,
`providers`, `plugins`, `docs`, `apps`, `.github/wiki`, and the root
`README.md`; any `testdata` or `.claude` path component, the two generated
outputs below, and `CHANGELOG.md` are always skipped.

A term's own `internal/migration/sweep/testdata/v1-terms.json` entry decides
its disposition: DONE (every declared replacement file exists and contains
the declared anchor literal — a missing file or anchor is a fail-closed input
error, exit 2, never a silent downgrade), OPEN (`state: "planned"`, not yet
stale — every replacement already present with its anchor is also a
fail-closed error, exit 2: advance the term to `"done"` instead), UNRESOLVED
(no declared replacement), or PROVENANCE (an accepted read-only citation of
the v1 archive as design evidence, listed by exact `(path, term)` in
`internal/migration/sweep/testdata/provenance-refs.json` — never a dropped
behavior; a listed pair with no matching hit is a stale reference, exit 2).

The inventory's decommission-safe column is `yes` only for a DONE row and
`no` for every other disposition, PROVENANCE included. A hit whose anchor
symbol is still referenced by non-migration production code (for example the
live v2 managed-block marker `markerOpenPrefix`) can never be `yes`: it is
declared PROVENANCE with a "live v2 marker" reason instead, and
`TestResolve_LiveAnchorNeverDecommissionSafe` fails the default CI lane on
any `yes` row whose anchor v2 production code still uses.

The term file is `{"v1_source": "<v1 commit sha>", "terms": [...]}`.
`v1_source` records the v1 commit the term set was derived from. Each term
entry carries these fields:

| field | meaning |
|---|---|
| `term` | the literal token scanned for (case-sensitive; unique in the file) |
| `class` | `command` (a v1 CLI command), `config_key` (a v1 config key) or `marker` (a design-provenance marker; declares no replacement) |
| `replacement` | slash paths of the tracked v2 files that implement the behaviour; empty means no declared replacement |
| `anchor` | a literal from every replacement file naming the behaviour; required exactly when `state` is `done` |
| `state` | `done`, `planned`, or `""` (empty exactly when `replacement` is empty) |
| `note` | a free-text explanation of the term and its replacement |

Each `provenance-refs.json` entry is `{"path", "term", "reason"}`; the reason
must say why the hit is not a v1 dependency.

The classified results are committed at
[`docs/migration/v1-pointer-inventory.md`](migration/v1-pointer-inventory.md)
(one row per `(pointer, path)`, no line numbers) and, for the UNRESOLVED
subset only, `docs/migration/unresolved.json`. `--dry-run` instead prints
every hit's `path:line` location plus a per-declared-term hit-count table
(zero counts included) and writes nothing. Exit codes: 0 success, 2
missing/malformed input (including an unrecognized flag such as the removed
`--planning`), 3 UNRESOLVED rows present under `--fail-on-unresolved`. The
sweep never deletes a v1 artifact; it only reports what remains to be
decommissioned once a real replacement lands.

Any ticket that adds or removes a v1-term mention regenerates both outputs
with one command and commits them:

```console
go run ./internal/migration/sweep --files <(git ls-files -z)
```
