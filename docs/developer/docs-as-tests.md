# Doc truth gate (`internal/build/doctruth*.go`)

Status: active from P1-DOC-07. Public docs are part of the tree's
contract with its readers: a link that 404s, a citation of a path that
moved, or a test name that was renamed is a bug in the same sense a
broken build is a bug. This gate turns that class of bug into something
that can fail.

## Scope

The gate scans the tracked public-doc universe: `README.md`,
`docs/**/*.md`, `.github/**/*.md`, `plugins/*/README.md`. Dated records
are excluded, since they describe a point in time rather than the current
tree: `docs/adrs/**`, `docs/releases/**`, `docs/spikes/**`,
`docs/waves/**`, and `CHANGELOG.md`. `DocTruthScope` computes this list
from `git ls-files`, never a filesystem walk, so a gitignored file can
never appear in it.

## The nine rules

- **link** — every markdown link (inline `[x](y)` and reference-style
  `[x][y]`), resolved relative to the page it's on, must point at a
  tracked file or directory. A `github.com/acamarata/cascade` `blob/`,
  `tree/`, or `wiki/` URL is mapped back to its repo-relative path first;
  any other host is counted but never fetched (external links are out of
  scope for existence checking — this gate never touches the network).
- **anchor** — a link's `#fragment` must match a heading's GitHub-style
  slug on the target page (lowercase, spaces to `-`, punctuation
  dropped, duplicate headings suffixed `-1`, `-2`, ...).
- **path** — a backticked token starting with `cmd/`, `internal/`,
  `pkg/`, `providers/`, `plugins/`, `docs/`, apps/, or `.github/` must
  name a tracked file or directory (apps/ is left unbackticked here on
  purpose: this repo has no apps/ tree yet, so a live citation of it
  would be exactly the kind of stale path this gate exists to catch — it
  is the eighth scanned prefix, reserved for when one lands). A token
  containing a wildcard character (`*`, `?`, `{`, `,`) is a pattern, not
  a citation, and is skipped.
- **line** — a backticked `path:N` or `path:N-M` citation must name a
  tracked file whose line count covers `N` (and `M`, if given).
- **symbol** — a backticked citation shaped like pkg/path.Ident or
  pkg/path.Type.Method (left unbackticked here since neither is a real
  declaration) must resolve to a top-level declaration in that
  directory's non-test `.go` files, via `go/parser`.
- **test** — a backticked `Test*`/`Fuzz*`/`Benchmark*`/`Example*` name
  must be declared as a top-level func in some tracked `_test.go` file
  anywhere in the tree.
- **claim** — outside fenced code blocks and inline-code spans, the
  case-sensitive stand-in markers (spelled out below, never live here)
  and a set of case-insensitive stale-forward-claim phrases are
  findings. The markers: `T`+`ODO`, `FIX`+`ME`, `TB`+`D`, `XX`+`X`,
  `PLACEHOLDER`. The phrases describe a promise about the future that a
  public doc should never make once it ships — "this will land later",
  "this isn't wired up yet", "this is waiting on its own ticket",
  "nothing works until some other ticket merges", "this page is a
  placeholder for now", "there is more of this to add later". A page that
  needs to say one of these things honestly should say so as a dated
  note under `docs/releases/` instead, which this gate never scans.
- **index** — three directories are required to list every page they
  hold: `.github/wiki/*.md` (except `Home.md`, `_Sidebar.md`,
  `_Footer.md`) must each be linked from `Home.md` or `_Sidebar.md`;
  `docs/security-posture/*.md` must each be linked from
  `docs/security-posture.md`; `docs/quickstart/*.md` (except its own
  README.md) must each be linked from that directory's README.md (left
  unbackticked here: docs/quickstart/ doesn't exist in this tree yet, so
  a live citation of its README would itself be a stale path). A missing
  index page itself is one finding, replacing the per-page findings that
  index would otherwise report.
- **directive** — see below.

## The `doctruth:illustrative` directive

A line that legitimately shows an illustrative, non-real path, line
citation, symbol, or test name (for example, a made-up example in a
tutorial) can carry:

```
<!-- doctruth:illustrative -->
```

anywhere on that same line. It suppresses **only** the path, line,
symbol, and test findings that line would otherwise produce. It never
suppresses a link, anchor, claim, or index finding — those rules don't
consult it at all. A directive on a line with nothing for it to suppress
is itself a finding (`directive` rule): the point is to keep the escape
hatch honest, not to let it become a blanket exemption stamp.

## CI mode vs release mode

`DocTruthCI` checks every finding against the committed ratchet baseline
(`internal/build/testdata/doctruth-baseline.json`, a sorted JSON array of
`{key, file, rule, detail}`). A finding whose key is in the baseline
passes; anything else fails. This exists so the gate can start enforcing
truth on day one without requiring every one of the tree's existing docs
to be fixed first — a strict gate that can only ever land fully green
would simply never land. The baseline can only shrink: nothing in this
package can add a key to it outside the one-time `baseline --init` run.

`DocTruthRelease` ignores the baseline and fails on every finding, full
stop. It's a Phase-completion check, not a per-commit gate — a doc
that's still catching up stays in the baseline until its owning ticket
lands, but the release certificate itself accepts no excuses.

Passing `DocTruthMode` any value other than `DocTruthCI` behaves exactly
like `DocTruthRelease` — the strictest reading wins on an unrecognized
value, never the most permissive.

## Running it

From the repo root:

```
go run ./internal/build/gen/doctruth check            # CI mode, exit 1 on any new finding
go run ./internal/build/gen/doctruth check --release   # release mode, exit 1 on any finding
go run ./internal/build/gen/doctruth check docs/foo.md  # scope the run to one file or directory
```

`check` prints one line per finding, `path:line: rule: detail`, with a
baselined finding (CI mode only) suffixed ` (baselined)`, followed by a
summary line: `checked N files, N links, N refs, N directives, N
findings`.

```
go run ./internal/build/gen/doctruth baseline          # prune: drop keys that no longer reproduce
go run ./internal/build/gen/doctruth baseline --init    # create the baseline for the first time
```

`baseline` is prune-only: it can only shrink the committed file to the
intersection of what's committed and what currently reproduces. It never
adds a key, even if the tree grew new findings since the file was last
written — that would defeat the ratchet. `baseline --init` is meant to
run exactly once, at this ticket's own tip; it refuses if the file
already exists.

```
go run ./internal/build/gen/doctruth guard <git-ref>
```

`guard` compares the baseline committed at `<git-ref>` against the
working copy and exits 1 if any key was added since then — the actual
ratchet check a reviewer or a CI job runs to confirm a change didn't
smuggle a new stale doc in under cover of an existing baseline entry.
The ship tooling itself never invokes `guard` or release mode; only its
own CI-mode `check` gates a normal merge.

## Nothing here is hand-edited

`internal/build/testdata/doctruth-baseline.json` is machine-generated —
by `baseline --init` once, and by the prune-only `baseline` afterward.
Don't add, remove, or reorder an entry by hand; run the command and
commit what it writes. The file is deterministic (sorted by key,
LF-terminated) so a regeneration that changes nothing produces an empty
diff.
