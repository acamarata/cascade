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

## Consolidation and staleness

Two background jobs maintain the store. Both run inside the daemon on the
cron scheduler, both take their instants from the injected clock, and both
are safe to run twice.

### What consolidation is allowed to do

Consolidation retires exact duplicates. It groups live records by
`(kind, BLAKE3 of the normalized body)` and merges only groups whose
bodies are byte-identical after normalization. Normalization folds line
endings (`\r\n` and `\r` to `\n`), strips trailing spaces and tabs from
each line, and trims leading and trailing blank lines. Nothing else folds:
a changed word, a changed character, a changed indent, or the same body
filed under a different kind all keep records apart.

Nothing similar is ever merged. Embedding-distance clustering exists only
behind `[memory].consolidation_embedding`, which is off by default and
which this build **refuses** rather than silently downgrading to
exact-hash grouping — the flag requires an index that is not present yet,
and a caller who switched it on believes near-duplicates are being merged.

The job never rewrites content. Every member of a group already says
exactly the same thing, so there is nothing to merge into the survivor:
the survivor's file is left untouched on disk and the others are
tombstoned. A background job can take a duplicate away; it can never
author a new sentence into a record the user wrote.

The survivor is the member with the earliest `CreatedAt`, ties broken by
canonical address. The oldest record is the one a user is most likely to
remember writing.

### Nothing is retired without an account of it

Before any member is tombstoned, the job writes

```
{base}/consolidations/{kind}/{survivor}.consolidation.json
```

holding the body every member shared and, for each retired record, its
address, description, scope reference, commit, supersedes reference,
confidence, origin, session, timestamps, content hash and TTL. A retired
record is fully reconstructible from that file. The account **accumulates**:
a later run that retires another duplicate into the same survivor unions
its members into the existing file rather than replacing it, and a file
that cannot be parsed is refused rather than overwritten.

### The crash contract

The order is: write the account, then tombstone each member (the store
itself writes a tombstone before removing a file), then emit the event.

* Interrupted before the account lands — the tree is exactly as it was.
  Every record is live, nothing to explain.
* Interrupted during the account write — the previous account survives,
  because the write is temp-plus-rename.
* Interrupted between tombstones — some members are retired and some are
  live, and the account already names every one of them. The next run
  finishes the job.

There is no state in which a half-written merge exists, because no content
is ever rewritten.

### Idempotency

A second run over an already-consolidated tree forms no group of two,
writes nothing, emits nothing, and returns
`ConsolidationReport{Merged: 0, NoChange: true}`. Grouping order comes from
a sorted key list, never from a map walk, so the same corpus always
produces the same result on any machine.

### Staleness is a heuristic and is treated as one

The staleness scan flags records that nothing has updated in longer than
`[memory].staleness_window_days` (30 days by default), judged on
`Provenance.UpdatedAt`, falling back to `CreatedAt` for a record that was
never rewritten. There is no recall-tracking field in the store, so a
record that is read constantly and never edited will be flagged. That is
precisely why the queue is advisory.

The scan has exactly one power: it writes addresses into
`{base}/staleness/{kind}.staleness.json`. It never edits a record, never
tombstones one, never changes a confidence, and nothing in this build acts
on the queue automatically. It is an input to review and a *candidate*
list for the forget pipeline — a candidate is something a person decides
about.

**It is reversible.** Each scan recomputes the whole set from the tree
rather than appending to what is already stored, so a record that has since
been updated is dropped from the queue on the next scan with no manual
step. Age is not wrongness, and "this looked old once" is not allowed to
harden into a standing judgement about a user's memory.

A scan that changes nothing writes no file and emits no event, and returns
`StalenessReport{Queued: 0, Idempotent: true}`.

### Events

| Event | Payload |
|---|---|
| `memory.consolidated` | `{member_ids, consolidated_id, method, consolidated_at}` — the retired addresses and the survivor's. |
| `memory.stale_queued` | `{stale_ids, queued_at}` — only the addresses *newly* queued by that scan. |

Both payloads carry addresses and counts, never a record's body. A bus
event fans out to every subscriber, and a subscriber that only asked to
know a consolidation happened must not receive the text of the records it
retired.

A sink failure is non-fatal for both jobs. By the time an event is offered
the work is already durable; failing the job afterwards would report work
as undone that is in fact done.

### Scheduling and exclusion

Both jobs register on the daemon's cron scheduler at startup, as
`memory-consolidate` and `memory-staleness`, at the schedules
`[memory].consolidation_schedule` and `[memory].staleness_schedule`
(`@every 24h0m0s` by default). Registration is real: the runnables the
production composition root builds are the ones the tests fire.

Exclusion between daemons is the scheduler's own advisory lock. A second
daemon over the same store never acquires the lease, never activates, and
therefore never ticks these jobs at all; a shared store across daemons is
unsupported. Neither job takes a second lock of its own, which would be a
parallel mechanism that could disagree with the first.

Within one process, `cascade memory consolidate` and the scheduled job
share one consolidator and one re-entrancy guard. A run that arrives while
another is in flight stands down and reports `skipped`, rather than racing
it or blocking a user's command behind a background job.

### Config

| Key | Default | Meaning |
|---|---|---|
| `memory.consolidation_schedule` | `@every 24h0m0s` | Cron spec for the consolidation job. |
| `memory.staleness_schedule` | `@every 24h0m0s` | Cron spec for the staleness scan. |
| `memory.staleness_window_days` | `30` | Age past which a record is flagged. |
| `memory.consolidation_embedding` | `false` | Embedding clustering. Refused by this build. |

A wrong type on any of these keys is a hard refusal of the section: a user
who wrote a window must never be told a different one is running. The
daemon logs the refusal and falls back to the documented defaults rather
than failing to start, so a typo in a maintenance window cannot stop the
memory store being served.

## Review queue

Promotion at the threshold is mechanical and stays that way. The review
queue is the complementary human lane: it shows what the mechanical lane
decided and what it has not decided, and it carries out the four actions a
person can take about that.

`cascade memory review` with no address only reads. It lists two sections:

- **Pending**: candidates still *below* the promotion threshold. They are
  listed because their counts are below it, which is a fact about the
  evidence, not a suggestion that they should be promoted. The thresholds
  in force are printed beside the counts so the claim can be checked rather
  than trusted.
- **Promoted**: promotions still standing. These are already durable
  records; they are listed because a revert is the only way to take one
  back, and that is not possible without knowing which records they were.

Three things the two tables do not show are still reported, because a queue
that renders as empty while work is waiting is telling a reader something
untrue: the number of pending candidates a live defer is hiding, the
candidates that have already crossed the threshold and belong to the
mechanical lane, and the addresses of candidate records that could not be
read.

Listing changes nothing. Neither does the digest. Nothing in this surface
promotes, retires or hides anything as a side effect of being viewed.

### Actions

An action is explicit and addressed. There is no bulk mode: an action flag
with no `<kind>/<name>` address is refused rather than applied to whatever
the listing happened to contain.

| Action | Flag | What it does |
|---|---|---|
| approve | `--auto-approve` | Promotes the candidate now, ahead of the mechanical threshold. |
| skip | `--auto-skip` | Leaves it exactly as it is. Recorded in the audit, changes nothing in the ledger. |
| defer | `--defer-days N` | Writes `SnoozeUntil`, hiding it from the queue for N days (7 by default, 365 at most). |
| revert | `--revert` | Takes back a promotion. |

`CASCADE_MEMORY_REVIEW_ACTION` selects the action when no flag does, for
callers that cannot pass flags. Flags win over it, and two actions at once
are a refusal rather than a precedence rule.

A defer changes *when* a candidate is shown and nothing else. Its counts,
sessions and status are untouched, so a deferred candidate that reaches the
threshold is still promoted mechanically on the same terms. Deferring is a
statement about a person's attention, not about the evidence.

A revert leaves the durable record in place. The candidate becomes
`reverted`, and its next observation restarts the count at one, so a
retracted belief has to earn the threshold again. Deleting the record the
promotion wrote is `cascade memory forget`'s job, and the revert output
says so.

Every action emits a typed audit event, including a skip: an audit that
recorded only the actions that changed something could not answer whether
anyone looked.

### Weekly digest

A scheduled job (`memory-review-digest`) builds a digest once a cadence and
publishes it on the event bus as `memory.review.weekly_digest`. It reads
and reports; it promotes nothing and writes nothing at all, so a daemon
that has fired it fifty-two times has changed no candidate by doing so.

The payload carries the window it speaks for (`since`, `until`), the
thresholds in force, the pending candidates, the promotions made *inside*
the window, and the addresses of anything unreadable. Every candidate is
reported as an address plus counts. No description, body or draft text ever
enters the payload, because a bus event fans out to every subscriber and a
subscriber that asked to know a review is due must not receive the text of
what is being reviewed.

The window is derived from the clock and the cadence, not from a stored
cursor. There is nothing to lose or corrupt, and re-running a digest for
the same instant recomputes the same answer byte for byte. An empty week is
published like any other: an event that appeared only when there was news
could not be told apart from a job that had stopped running.

Publishing to the local bus is the whole of the delivery this build
performs. No mail is sent, no webhook is called, and no bridge is notified;
anything outbound is a subscriber's decision.

### Config

| Key | Default | Meaning |
|---|---|---|
| `memory.review_cadence_days` | `7` | Digest period, in whole days. |

The cadence is one key rather than a schedule beside a window, because the
digest reports on exactly the stretch of time since its previous fire. Two
keys could be set to disagree, and a window shorter than the period would
silently skip promotions. A cadence that is not a positive whole number of
days is refused rather than read as "off": a user who wants no digest
disables the job.

## Forget pipeline

`cascade memory forget <kind>/<name>` is the one verb in this system that
destroys a user's own record. It runs a pipeline rather than a delete, and
the pipeline's contract is not "the record is gone" but "here is every
place this record left a mark, and here is what happened to each".

### What it does, in order

1. **Account first.** Before anything is removed, an account is written to
   `{base}/forgotten/{kind}/{name}.forget.json`, carrying the entity id,
   the reason, the request instant and the schema version. Every later
   state is therefore legible on disk: an interrupted forget says which
   address it was retiring and how far it got.
2. **Index second.** The projected row, its full-text postings and its
   vector are removed through the projection's `ScrubRecord`.
3. **Record third.** The store writes the tombstone and unlinks the file,
   in that order, so the deletion is durable from the moment the tombstone
   lands.
4. **Event last.** A `memory.forgotten` event carrying the address, the
   instant and the reason is published, so the backup and sync lane can
   exclude the entity from an incremental export. Without it, a restore
   would bring a forgotten record back.

The index is scrubbed **before** the file is removed, not after. A crash in
that window leaves a record that is still on disk and still returned by the
file-scanning verbs, with only a derived index briefly behind the files,
which is the state this system already treats as normal and repairs on the
next projection run. The reverse order would leave the opposite: a record
the user has been told is gone, still answering searches out of a stale row
that carries its body.

### Idempotence and resumption

A second forget of a completed address does nothing and reports
`already_forgotten`. A forget of an address whose account is incomplete
resumes: the scrub removes nothing when there is nothing to remove, the
delete is skipped when the record is already gone, and the event is emitted
only if the account says it has not been. No interruption leaves work that
a later call cannot finish. The account is only marked complete once every
step, the event included, has succeeded.

### What survives a forget

The verb reports all of this on every call, so nothing has to be inferred
from silence.

| Place | Disposition | Why |
|---|---|---|
| Record file | removed | Unlinked after its tombstone is written. |
| Projection row and postings | removed | Retracted exactly, from the row's own stored token set. |
| Vector index entry | removed | Deleted by address. |
| Tombstone | kept | Removing it would bring the record back on the next scan. |
| Forget account | kept | Records the address, time and reason, and never the record's text. |
| Consolidation account | kept | It explains what happened to *other* records; editing it would take their explanation away. |
| Staleness queue | kept | Holds addresses only; the next scan recomputes it without this record. |
| Backup and sync note | kept | The event is the point: it is what stops a restore returning the record. |
| Candidate ledger and review queue | **unreachable** | A candidate for this address keeps its draft, which repeats the record's text. The ledger contract has no delete for this pipeline to call. |
| Record bytes on disk | **unreachable** | The file is unlinked, not shredded. The bytes may remain recoverable from the file system, a backup or a snapshot. |

A forget of one record changes exactly two things in the tree besides the
account: the record file goes and its tombstone appears. Lexical
neighbours, records of other kinds, records of the same name in another
kind, consolidation accounts and the review queue are untouched.

When no index is wired to the pipeline, the two index rows above report
`not_configured` rather than `removed`. That is not the same claim as "the
index was clean", and the verb does not make the stronger one.

### Verification, and the orphan check

The scrub is verified by re-running it: a second `ScrubRecord` of the same
address that removes nothing is a direct assertion of absence, rather than
trust in the first call's return code.

`ProjectionJob.Orphans` is the doctor check for the whole store: it reports
every projected row still answering queries for a record the files no
longer hold. That is this system's equivalent of an orphaned blob, and
finding one means a forget was interrupted between removing the file and
scrubbing the index, or that the projection has not run since a record was
deleted outside the store. A row already marked retired is not an orphan:
its body and postings are cleared and it answers nothing.
