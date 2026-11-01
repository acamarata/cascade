# Real claude CLI hook-event capture (contract:cc-hook-capture)

`events.json` is the hook-event map bundled in the installed claude CLI
binary, extracted verbatim. Each entry is
`{event, summary, description, exit2_line, other_exit_line}`.

## Provenance

- Tool: claude CLI CLI (native install)
- Version: 2.1.316 (`claude --version` prints `2.1.316 (claude CLI)`)
- Binary sha256: `64c327e9 2b2c5e0f 78c0a980 c5a51812 1e1f0d14 0ca771ca 5dd4488d 1a2dcb3f`
  (written in eight groups of eight hex digits; remove the spaces to compare)
- Capture date: 2026-11-01
- Method: the binary was read as raw bytes; the CLI was not run to produce
  this file. The version line was read with `env -i PATH=/usr/bin:/bin
  HOME=<fresh temp dir>` from a fresh, empty temp working directory, with no
  credential variable set.

## Extraction

A throwaway Go program (kept out of the repository) did this:

1. Read the binary as bytes and require the literal `{PreToolUse:{summary:`
   to occur exactly once.
2. From that offset, scan to the balanced closing brace, skipping braces
   inside `"`, `'` and backtick string literals.
3. Take every key matching `[{,]([A-Z][A-Za-z]+):\{summary:` inside that
   object, in source order. The map has no `hook_event_name` field; the event
   is the object key. Exactly 33 keys matched (none is `WithheldNotice`).
4. For each key, decode the `summary` and `description` string literals
   (`\n`, `\t` and `\uXXXX` escapes decoded, nothing else changed).
5. `exit2_line` is the text after `Exit code 2 -` up to the newline, trimmed;
   19 events have one. `other_exit_line` is the text after
   `Other exit codes -` likewise. WorktreeCreate has no exit-2 line; its rule
   is its `Other exit codes -` line (`worktree creation failed`).
6. Write the list as JSON with two-space indent.

## Scrub rules

The map holds no user path, host or account value. The scrub check is that
the file contains no macOS or Linux home-directory
path or email address;
`TestRealCCCapturesScrubbed` in `internal/context/generate` enforces it, and
`TestHookCaptureShape` holds the counts at 33 events and 19 exit-2 lines.
Tests replay this file and never run a real harness CLI.
