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

## Candidate ledger

Not everything worth noticing is worth remembering. An observation starts
as a candidate: one file per candidate under `candidates/<kind>/`, holding
the sessions that have referenced it, the count those references add up to,
its status, and the draft record it would become. Candidates live beside
the record tree rather than inside it, so nothing walking the store can
mistake a candidate for something the system already believes.

A candidate is `pending` until it is promoted, `promoted` once a durable
record has been written from it, and `reverted` once a promotion has been
taken back.

The counting rule is deliberately hard to game. Recording the same
observation many times inside one session counts once: the session is what
makes an observation distinct, so the count tracks how many sessions have
independently said the same thing. The session list is a sorted set, so the
stored file and every decision taken from it are the same whatever order
the observations arrived in.

Evidence that cannot be read is never treated as no evidence. A candidate
file this build cannot parse is refused, and a file declaring a newer
format version is refused as unsupported rather than read on a guess.
Either way nothing is promoted from it, because a wrong guess here would
write a false belief that then persists.

## Promotion ladder

A candidate is promoted once it has been referenced at least three times
across at least two distinct sessions. Promotion is mechanical: no model is
asked and no one is prompted, and the same sequence of observations always
produces the same promotions. Both conditions are checked because they ask
different questions. The reference count asks whether the observation
recurred; the session count asks whether it recurred anywhere other than
the conversation that first produced it, since a belief attested only
inside one session is the one most likely to be an artifact of it.

Promotion writes the durable record first and marks the candidate promoted
second. An interruption between the two leaves a candidate that can be
promoted again, rather than one claiming a record that was never written.
Once a candidate is promoted, further observations of it change nothing,
write nothing and emit nothing, so a repetitive caller cannot rewrite a
durable record through the ladder.

Reverting takes a promotion back. The candidate becomes `reverted` and
keeps its counts, the reason, and the time, so a promotion someone later
asks about can still be accounted for. Reverting does not delete the
durable record: removing that is the forget path's decision, not this one's.
The next observation of a reverted candidate restarts the count at one with
only the observing session, so a candidate that was taken back has to earn
the threshold again rather than slipping back through on the count it had
before.

Both transitions emit a typed event, so no promotion or revert happens
silently.

## DB projection

Scanning every file answers a recall query correctly and slowly: the cost
grows with the size of the store, and every query pays it again. The
projection is the fast path. It reads the tree and writes a row per record
into the memory domain of the local database, with a full-text posting set
and an embedding, so a query touches an index instead of the file system.

It is derived state under the rule above: delete it, corrupt it, or open a
store that never had one, and a run puts it back from the files alone.

### What a run does

A run walks every kind, reads each live record, and compares it with the
row it already has:

| Situation | What happens |
|---|---|
| No row yet | The row, its postings and its embedding are written. |
| Row matches the record exactly | Nothing is written. A second run over an unchanged store writes nothing at all. |
| Body changed | The row and postings are rewritten and the record is embedded again. |
| Only a description, scope or confidence changed | The row and postings are rewritten. The embedding is left alone, because the text it was computed from did not change. |
| Record tombstoned, or its file removed outright | The row is marked deleted, and its postings and its vector are removed. |
| Record cannot be read | Nothing is indexed, any row it had is withdrawn, and the run reports the refusal. |

The comparison covers the whole row, not just the body hash, for the same
reason a write to a file does: treating a changed description as "no
change" would leave the index disagreeing with the file.

Deletion is noticed by comparing the live listing with the rows on hand,
rather than by looking for tombstone files. That catches a tombstone and
also catches a record file deleted with an ordinary `rm`, which leaves no
tombstone behind.

### Rebuilds

The projection carries a layout version. When the version stamped in the
database is not the one the running build writes, a run does not try to
patch rows written under a layout it does not know: it drops the whole
projection and rebuilds. That is always available, because the files hold
everything, and it is idempotent, so two rebuilds over the same tree
produce identical bytes.

### The index cannot show you more than the store would

A record the store refuses to read, because it is damaged or declares a
format version this build does not know, is refused here too, and any row
it previously had is withdrawn. Retired records and records whose expiry
has passed are excluded from results, judged against the same injected
clock the rest of the store uses. Each record's scope travels with its row,
so a hit can never escape the scope its own file declares.

A refusal is reported, never silent. One unreadable file costs exactly its
own record: the run continues over the rest of the store and names what it
could not project, in the same spirit as a listing that parses nothing so
one bad file cannot make a whole kind unlistable.

### Recall integration

A hit is a pointer, not an authority. The body stored in a row is what the
file said when it was last projected, and if the two disagree the file
wins. Recall uses the projection to find candidates quickly, in two legs
over the same rows: the postings answer term queries, and the embeddings
written alongside them answer similarity queries, so a memory record takes
part in hybrid recall from the moment it is written. Ranked results are
ordered deterministically, so the same query over the same store returns
the same answer on any machine.

## SOUL store

The SOUL is the persistent identity document: the system's own model of the
person it serves. It lives in two files under `{CASCADE_HOME}/memory/soul/`.

| File | What it holds | Who writes it |
|---|---|---|
| `SOUL.md` | The document body, plain markdown, no header | The store, and the user's own editor |
| `soul-ledger.json` | Format version, version counter, content digest, reconcile pointer, audit log | The store only |

The body is a plain file with no frontmatter because it is the one file a
person is expected to open and edit. Everything machine-managed lives in the
ledger beside it, out of the way.

### Three routes, one write path

A change reaches the SOUL by exactly three routes, and every one of them
calls the same internal `applyEdit`. That function is the only place in the
package that moves the version counter or appends to the audit log.

| Route | Reached by | Recorded as |
|---|---|---|
| a | `cascade memory soul edit` (or `--content <file>`) | `cli` |
| b | An edit made to `SOUL.md` in any editor, adopted on load | `file-reconcile` |
| c | `SoulStore.EditViaChat`, the chat-mediated API | `chat` |

There is no fourth route. Every accepted write increases the version by
exactly one and appends exactly one audit entry, and the version is
monotonic *across* routes, not per route.

### Version semantics and the audit log

An audit entry is `{version, route, edited_at, delta_hash}` and carries no
document text at all. `delta_hash` is the BLAKE3 digest of
`"<previous content hash>:<new content hash>"` — the digest of the
*transition*, not of the content — so the log is verifiable (the same change
always hashes the same way) while the document is not recoverable from it.
This matters because the whole log ships in every export: an entry that
carried a body or a diff would leak the user's identity document twice.

### Divergence detection (route b)

`SoulStore.Get` and `DetectDivergence` hash the file on disk and compare it
against the digest the ledger recorded. Three outcomes:

- **Agree.** Nothing moves. No version, no entry, no event, no note. Only the
  bookkeeping pointer that records "the two sides were seen to agree".
- **Clean external edit** (the file changed; the store's last write had been
  seen on disk). The file's content is what the user meant, so it is adopted
  through `applyEdit(route=file-reconcile)`: version bumped, entry appended.
  The user's own edit becomes a recorded change rather than an untracked one.
- **Conflict** (the file changed *and* the store's last write had not been
  seen on disk). Both sides may have moved. Nothing is merged, adopted or
  discarded: a typed `memory.soul.diverged` event goes to the bus, a
  `divergence-note.json` is left for `cascade doctor`, and the call refuses
  with `ErrSoulDiverged`. `soul show` reports it and still prints the file.

Neither the event nor the note carries soul text or a machine path; both name
versions, digests and an instant, because both are read by things that have
no business seeing the document.

A conflict is not a dead end. Route (b) refuses to resolve one on its own,
but an explicit write — `soul edit` or the chat API — is a person saying what
the document should now say, so it is honoured, and the divergence is on the
bus and in the note before that write lands.

### Write ordering

`applyEdit` writes `SOUL.md` first and the ledger second. The reverse order
would leave a ledger claiming a version and an audit entry for content that
is not on disk: a confident but *wrong* model of the user. With this order an
interruption leaves real content on disk and a ledger that has not claimed
it, which the next read reports as a divergence — the content survives and
nothing is claimed that was not written.

### Export

`cascade memory soul export` produces exactly this envelope and nothing else:

```json
{
  "schema_version": 1,
  "exported_at": "2026-03-01T12:00:00Z",
  "soul": { "body": "…", "schema": "cascade.soul/v1" },
  "audit_entries": [
    { "version": 1, "route": "cli", "edited_at": "…", "delta_hash": "…" }
  ]
}
```

No other memory record, no file path, no environment or machine detail. This
is the most personal file cascade produces, so the field set is asserted in
tests against the actual exported bytes, with canaries planted in the same
store and the same directory to prove none of them travels with it.

### Automation parity

`soul edit` is the only interactive verb in the memory tree. Its
non-interactive equivalent is `soul edit --content <file>`, which opens no
editor and reads no environment. With `CASCADE_NO_INPUT=1` set, opening an
editor is a hard error raised *before* any subprocess is created, so an
automated run gets a refusal it can read instead of a process waiting on a
terminal that is not there.
