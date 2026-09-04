# Memory architecture

## Files-first store

Memory is kept in files. Not in a database with an export button, and not
in an opaque format: one memory record is one markdown file you can open,
read, edit, diff and commit with the tools you already use.

### Layout

```
{base}/
  user/
    units-and-clock.md
  feedback/
    read-before-summarising.md
    retired-note.md.tombstone
  project/
    index-rebuild-direction.md
  reference/
    rate-limits.md
```

The first directory level is the record's kind, one of four:

| Kind | Holds |
|---|---|
| `user` | Durable facts about the person the system serves. |
| `feedback` | Corrections and stated preferences. |
| `project` | Project state, decisions and lessons. |
| `reference` | Looked-up material worth keeping. |

The filename stem is the record's name, and the kind plus the name is the
record's identity. Names are restricted to ASCII letters, digits, `.`, `_`
and `-`, up to 128 bytes, and may not begin with a dot, end with a dot or a
dash, or be a name Windows reserves for a device. The restriction is what
keeps a record's location on disk the same on every platform, and keeps a
name from being usable to reach outside the store.

### File format

Each record is a frontmatter block fenced by `---`, then the markdown body.

```markdown
---
format: 1
name: "units-and-clock"
kind: "user"
description: "Stated unit and clock preferences"
scope_ref: "global"
commit_sha: ""
supersedes: ""
expires_at: ""
confidence: 0.9
origin: "session"
session_id: "0f9c1d2e-4a5b-6c7d-8e9f-a0b1c2d3e4f5"
created_at: "2026-01-02T03:04:05Z"
updated_at: "2026-01-02T03:04:05Z"
content_hash: "aa874ea8d9ab7dca40dce19fe9e287d315527a30d1cbbdff7e61beae7ce19eb5"
---
Prefers metric units and a 24-hour clock.
```

| Field | Meaning |
|---|---|
| `format` | On-disk format version. A file declaring a version this build does not know is refused rather than read on a guess. |
| `name`, `kind` | The record's identity, matching its path. |
| `description` | A one-line summary. |
| `scope_ref` | The scope the record belongs to. |
| `commit_sha` | Optional commit the record is pinned to. |
| `supersedes` | Optional `<kind>/<name>` reference to the record this one replaces. |
| `expires_at` | Optional expiry. Empty means the record does not expire. |
| `confidence` | How much to trust the record, from 0 to 1. |
| `origin`, `session_id` | Where the record came from: `session`, `file` or `harness`. |
| `created_at`, `updated_at` | When the store first wrote and last rewrote the record. |
| `content_hash` | BLAKE3 digest of the body as of that last write. |

The header is a strict subset of YAML rather than the whole language.
Every string value is a quoted literal and the version is a bare integer,
so nothing is left to type inference: a description of `no`, `1.0` or
`2026-01-01` is text, not a boolean, a float or a date. Every key is
present on every record, in a fixed order, so two records with the same
content produce identical bytes on any machine.

Everything after the closing fence is the body, verbatim. A body may
contain `---` lines of its own; only the first fence after the header ends
the block.

### Provenance and drift

`content_hash` is a snapshot, not a live checksum. It is the digest of the
body at the moment the store last wrote the record. Because you are meant
to edit these files directly, the body can move on without the store being
told, and when it does the stored hash and the body's current hash differ.

That difference is the drift signal, not an error. It is how anything
derived from a record, such as a search index, knows which records need to
be re-derived and which can be left alone. Reading a record whose body has
been edited outside the store succeeds normally.

### Writing is idempotent

Writing a record that already matches what is stored changes nothing: the
file is not rewritten, and its modification time does not move. A write
that does change something, whether the body or any other field, rewrites
the file and moves `updated_at` forward.

The comparison covers the whole record, not just the body. Changing only a
description or a confidence is a real change and is persisted.

### Deleting leaves a tombstone

Deleting a record writes an empty `{name}.md.tombstone` marker beside it
and then removes the record file. A reader treats the tombstone as
authoritative: a tombstoned record does not exist, does not appear in a
listing, and cannot be read, even if the record file is still there.

Deletion is a marker rather than a plain removal so that anything derived
from the tree learns of the deletion by scanning it, without needing a
previous listing to compare against. Writing the same name again removes
the tombstone and brings the name back into use.

## What is authoritative

The files are. Anything else that holds record data, including a search
index or a database projection, is derived state: it can be deleted and
rebuilt by walking the tree, and nothing it holds is needed to reconstruct
a record.

When a derived copy disagrees with a file, the file is right and the copy
is stale. The response is to rebuild the copy. It is never to write the
file back from the copy.

## Failure behaviour

**Reads refuse what they cannot parse.** A record that is truncated, has a
damaged header, carries a value of the wrong type, or declares an unknown
format version is refused whole. No read returns a partly-filled record,
and no read repairs a file by guessing at what it meant.

**One bad file affects one record.** Listing reads directory names and
parses nothing, so a damaged record does not hide its neighbours or make
its kind unlistable. The damaged record can still be deleted, which is the
way out of a file this build cannot read.

**A write will not overwrite a file it could not read.** The file may have
been written by a newer version, and destroying it to replace it would turn
a recoverable situation into data loss.

## Durability

Every write goes to a temporary file in the target's own directory, is
flushed to storage, and is then renamed into place. An interruption at any
point leaves either the previous record intact or nothing new, never a
half-written file. The temporary file shares a directory with its target
deliberately: a rename within one directory replaces the target atomically,
while a rename across volumes does not, and on Windows may be refused
outright.

A delete writes the tombstone before removing the record, so the deletion
takes effect the moment the tombstone lands. An interruption between the
two steps leaves both files present, and the tombstone wins.

## Determinism

Timestamps come from an injected clock and are stored in UTC, so a record
written in one time zone reads back as the same instant in another.
Encoding writes a fixed key order with no map iteration reaching the file.
Listings are lexically ordered. The content hash is BLAKE3, implemented in
pure Go, and depends on nothing but the bytes it is given, so the same body
produces the same digest on every platform and every run.
