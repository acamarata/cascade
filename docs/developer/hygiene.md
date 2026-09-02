# Commit hygiene: conventional commits and the identifier sweep

Status: active from Wave 1 (P1-E01-W1-S01-T5). This repo is public
(cascade PRI hard rule 3): no personal account or lane identifier may land
in a tracked file or a commit message. Machine enforcement lives in
`internal/build/commits.go` and `internal/build/sweep.go`, run two ways —
locally via a pre-push hook, and in CI (`.github/workflows/ci.yml`,
`hygiene` job).

## Commit message convention

Every non-merge commit's header line must match:

```
type(scope)?: subject
```

`type` is one of: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`,
`perf`, `ci`, `build`, `revert` (`internal/build/commits.go`'s
`CommitTypes`). `scope` is optional, parenthesized, and may contain
letters, digits, `_.-/`. A trailing `!` before the colon marks a breaking
change. `subject` must be non-empty. Body and trailer lines (a
`Signed-off-by:` line, for example) are never format-checked — only the
header.

Merge commits (more than one parent) are always skipped; the checker never
expects `Merge branch 'x' into y` to parse as conventional.

The checker runs over a **range** of commits, not just `HEAD` — a
multi-commit push is fully checked, not just its tip.

## Identifier sweep: what it does, and its one hard limitation

The sweep scans every **tracked** file's content (`git ls-files`, never an
untracked/gitignored file) and every commit message in the same range,
line by line, against a list of regular expressions.

**The deny-pattern list itself is never committed to this repo.** Writing
real personal account/lane identifiers into a tracked file to check
against would itself violate the rule the sweep exists to enforce. The
pattern list comes from exactly one of two external sources, resolved in
this order by `build.LoadPatterns`:

1. **Local dev machines** — an untracked file, one RE2 pattern per line
   (blank lines and `#` comments ignored), pointed to by
   `CASCADE_IDENTIFIER_PATTERNS_FILE` (default:
   `.claude/hygiene/identifier-patterns.txt` — under the already-gitignored
   `.claude/` tree).
2. **CI** — the same one-pattern-per-line format, inline, in the masked
   variable/secret `CASCADE_IDENTIFIER_PATTERNS`.

If neither source is configured, or the configured one is unreadable or
parses to zero usable patterns, the sweep **fails closed** — it blocks the
push/CI run, it never silently skips the check.

The sweep ships with **zero hardcoded patterns of its own.** An earlier
draft added two "structural" built-ins (any `/Users/<name>` path, any
`@gmail.com`-shaped address) as always-on defense in depth. Probing them
against the real tree immediately false-positived on
`docs/cli-reference/config.md`'s documented example paths
(`/Users/me/.cascade/...`) — legitimate tracked content, not a leak. A
hardcoded pattern with no allowlist mechanism either has to special-case
content it doesn't understand, or it blocks every push once wired in — so
this gate has none, and the external pattern source is the only thing that
decides what a "leak" looks like in this repo.

### What the sweep does NOT catch

- An identifier absent from the loaded pattern set — the sweep is only as
  good as its pattern source.
- Obfuscated, base64/rot13-encoded, or whitespace-mangled identifiers.
- An identifier embedded in binary content or image/EXIF metadata (a
  non-UTF8 file read is a hard error — fail closed — not a silent skip).
- History outside the scanned commit range or the current tree — a prior
  push that already leaked an identifier is not retroactively caught.

**A green sweep is proof the configured patterns found nothing. It is
never proof no identifier exists.**

## Installing the pre-push hook

```
git config core.hooksPath .github/hooks
```

One-time, per clone. The hook (`.github/hooks/pre-push`) runs both gates
over the outgoing range for every pushed ref before letting the push
through; a ref deletion (`local-sha` all zeros) is skipped, and a
brand-new remote ref (`remote-sha` all zeros) checks every commit
reachable from the local tip, since nothing on the remote bounds the
range yet.

Set `CASCADE_IDENTIFIER_PATTERNS_FILE` in your shell profile if your local
pattern file lives somewhere other than the default
`.claude/hygiene/identifier-patterns.txt`.

`go`/`git` are wrapped by `rtk` in some interactive dev environments in a
way that does not track the real exit code (R-14.114); the hook uses
`rtk proxy go` when `rtk` is on `PATH`, and the real `go` binary otherwise.

## CI wiring

The `hygiene` job in `.github/workflows/ci.yml` computes the same kind of
range (the pushed commits, or a pull request's commits against its base)
and runs the identical `go test -run ... ./internal/build/` invocations
the hook runs, with `CASCADE_IDENTIFIER_PATTERNS` supplied from repo
secrets. It is a required lane in `ci-gate`.
