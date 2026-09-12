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
