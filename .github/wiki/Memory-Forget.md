# Forgetting a memory

`cascade memory forget <kind>/<name>` retires one record. It is the only
command in the memory system that destroys something you wrote, it never
prompts, and it tells you exactly what it did.

```
cascade memory forget project/estuary --reason "no longer true"
cascade memory forget project/estuary --dry-run
```

## What the output tells you

The command prints a table of every place the record left a mark. Three
dispositions appear:

- **removed**: the mark is gone.
- **retained**: the mark is kept on purpose, and the row says why.
- **unreachable**: the mark exists and this command cannot remove it.

The last one is the important one. Two marks are always unreachable:

- **The bytes on disk.** The file is unlinked, not shredded. The bytes may
  still be recoverable from the file system, from a backup, or from a
  snapshot. Forgetting a record is not the same as destroying the data, and
  the command will not claim otherwise.
- **A candidate draft.** If the record was promoted from a candidate, the
  candidate keeps the draft it was promoted from, and that draft repeats
  the record's text. Nothing in this command removes it.

## What is kept on purpose

The tombstone stays, because removing it would bring the record back the
next time the store is scanned. An account of the retirement stays at
`memory/forgotten/<kind>/<name>.forget.json`, holding the address, the time
and your reason, and never the record's text. A consolidation account
stays, because it explains what happened to other records and editing it
would take their explanation away.

## What it does not touch

Nothing else. Not the record next to it in the alphabet, not a record of
the same name in another kind, not another kind at all, and not the review
queue.

## If it is interrupted

The account file is written before anything is removed, so an interrupted
forget is always recorded rather than silent. Running the command again
finishes the job: it never retires a record twice, never writes a second
tombstone, and sends the backup note only if it was not sent already.

## Restores

A completed forget publishes a `memory.forgotten` event. The backup and
sync lane reads it to keep the record out of an incremental export, which
is what stops a restore returning something you asked to be rid of. If that
event could not be delivered, the command says so on the line under the
summary rather than leaving you to find out at the next restore.
