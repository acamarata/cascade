# /archive — Archive Sessions and Clean Memory

Archives completed sessions, compresses old memory files, and cleans
temporary directories.

## What it does

1. Scans `.cascade/memory/threads/` for threads with status `done`.
2. Moves done threads to `.cascade/archive/threads/` with a datestamp.
3. Compresses memory files older than 30 days into monthly archives.
4. Removes stale files from `.cascade/temp/` (older than 7 days).
5. Updates the memory index after archiving.

## When to use

- Periodically (e.g., monthly) to keep memory lean.
- After a project phase completes and all threads are done.
- When `.cascade/memory/` grows large and recall performance degrades.

## Safety

Archive is non-destructive: files are moved, never deleted. Restore from
`.cascade/archive/` at any time.

## Related

`/recall` — search memory files.
`/threads` — view active threads.
