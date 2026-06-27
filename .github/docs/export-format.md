# Cascade Archive Format (`.cascade-archive.tar.gz`)

This document describes the portable export format produced by `cascade export` and consumed by `cascade import --from-export`. It is designed to be readable and restorable without Cascade installed.

## Overview

A Cascade archive is a standard `.tar.gz` file. Any standard `tar` and `gzip` toolchain can inspect or extract it:

```sh
# List contents
tar -tzf .cascade-archive.tar.gz

# Extract manually to a temp dir
mkdir /tmp/cascade-restore
tar -xzf .cascade-archive.tar.gz -C /tmp/cascade-restore
```

## File Layout

```
.cascade-archive.tar.gz
├── manifest.json          # Archive metadata (always the first entry)
├── memory/
│   ├── decisions.md
│   ├── lessons.md
│   └── patterns.md
├── inbox/
│   └── *.md
├── docs/
│   └── *.md
├── phases/
│   └── ...
└── (other ~/.cascade/ contents)
```

Every path in the archive is relative to `~/.cascade/`. When restoring, each entry is placed under the destination directory (default: `~/.cascade/`).

## manifest.json

The first entry in the archive is always `manifest.json`. It describes the archive contents and allows validation before extraction.

### Schema

```json
{
  "format_version": 1,
  "created_at": 1719446400,
  "content_hash": "blake3-hex-64-chars...",
  "files": [
    "memory/decisions.md",
    "memory/lessons.md",
    "inbox/msg-001.md"
  ],
  "secrets_included": false
}
```

| Field | Type | Description |
|---|---|---|
| `format_version` | integer | Layout version. Currently `1`. A future breaking change bumps this. |
| `created_at` | integer | Unix timestamp (seconds since 1970-01-01T00:00:00Z) when the archive was created. |
| `content_hash` | string | BLAKE3 hex digest of the archive bytes. Stored as a convenience; the authoritative value lives in the `.sha` sidecar file. |
| `files` | string[] | Relative paths of every non-manifest file in the archive. |
| `secrets_included` | boolean | `true` if `cascade export --include-secrets` was used. |

## Integrity Sidecar

Alongside the archive, `cascade export` writes a `.sha` sidecar file:

```
.cascade-archive.tar.gz
.cascade-archive.tar.gz.sha   ← BLAKE3 hex digest of the .tar.gz bytes
```

`cascade import --from-export` checks this sidecar before extracting. If the sidecar is absent, integrity verification is skipped with a warning. If the sidecar is present and the hash does not match, import is refused.

To verify manually:

```sh
# Using b3sum (https://github.com/BLAKE3-team/BLAKE3)
b3sum .cascade-archive.tar.gz
cat .cascade-archive.tar.gz.sha
# Both lines should show the same 64-char hex digest.
```

## Secrets Policy

By default, `cascade export` excludes files that look like credentials:

- Files named `vault.env`, `.env`, or starting with `credentials`
- Files with extensions: `.key`, `.pem`, `.p12`, `.pfx`, `.crt`, `.cer`

Pass `--include-secrets` to include these files. The `secrets_included` flag in `manifest.json` records which mode was used.

**Warning:** archives with `secrets_included: true` contain live API keys. Store them encrypted and never commit them to version control.

## Format Version History

| Version | Released | Changes |
|---|---|---|
| 1 | 2026-06-27 | Initial format. tar.gz + manifest.json + BLAKE3 sidecar. |

## Restoring Without Cascade

If Cascade is not installed, you can restore manually:

```sh
# Extract to a temp dir
mkdir /tmp/restore
tar -xzf .cascade-archive.tar.gz -C /tmp/restore

# Verify the manifest
cat /tmp/restore/manifest.json | python3 -m json.tool

# Move files to ~/.cascade/
cp -r /tmp/restore/. ~/.cascade/
```

This requires only standard `tar`, `gzip`, and a JSON viewer — no Cascade binary needed.
