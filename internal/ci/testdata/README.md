# internal/ci fixture provenance

Art.2 (12-QUALITY-CONSTITUTION.md) requires that at least one test exercise a
fixture captured FROM the real GitHub Actions REST API, never a self-authored
dialect. Every file in `fixtures/` below was captured that way. Nothing in this
directory was hand-written to look like a GitHub response.

## Capture method

| Field | Value |
|---|---|
| Tool | `gh api` (GitHub CLI) |
| Tool version | `gh version 2.78.0 (2025-08-21)` |
| Capture date | 2026-09-11 (UTC) |
| Source repository | `acamarata/cascade` (this repo's own CI history) |
| API host | `api.github.com`, REST, Accept `application/vnd.github+json` |

Bodies are stored byte-for-byte as the API returned them. The only edit applied
to any file is CRLF -> LF normalization on the two `.txt` header captures, so
that `.gitattributes`' `* text=auto eol=lf` does not later rewrite them and
invalidate the capture.

## Files

| File | Request | Why it exists |
|---|---|---|
| `runs_success.json` | `GET /repos/acamarata/cascade/actions/runs?status=success&per_page=1` | conclusion `success` |
| `runs_failure.json` | `GET /repos/acamarata/cascade/actions/runs?status=failure&per_page=1` | conclusion `failure` |
| `runs_cancelled.json` | `GET /repos/acamarata/cascade/actions/runs?status=cancelled&per_page=1` | conclusion `cancelled` |
| `run_jobs.json` | `GET /repos/acamarata/cascade/actions/runs/34616832399/jobs?per_page=100` | 18 real jobs with their `steps` arrays; job and step conclusions `success`, `failure`, `skipped` |
| `runs_200_headers.txt` | `GET /repos/.../actions/runs?per_page=3` (response headers only) | carries the real `Etag` the conditional-request path sends back |
| `runs_304_headers.txt` | the same request replayed with `If-None-Match: <that Etag>` | a genuine `HTTP/2.0 304 Not Modified`, for the "no domain write on 304" assertion |

`runs_200_headers.txt` and `runs_304_headers.txt` are a matched pair captured
seconds apart: the ETag in the first is the exact value sent in the second.

## Enum values NOT covered by a real fixture — read this before adding one

The capture swept all 258 workflow runs in this repository's history. Across
every one of them:

- `status` was **only ever** `completed`.
- run-level `conclusion` was **only ever** `success`, `failure`, or `cancelled`.

So the API documents these values that this repo has never actually produced,
and for which **no real fixture exists here**:

- `status`: `queued`, `in_progress`
- `conclusion`: `timed_out`, `skipped` (at run level), `action_required`, `neutral`, `stale`

Tests covering those values must construct them explicitly in Go and must not
pretend otherwise. Do NOT hand-edit one of the JSON files above to fabricate a
`timed_out` run and leave it sitting in `fixtures/` — that would convert a real
capture into a self-authored dialect and defeat the entire point of Art.2. The
real fixtures anchor the dialect (field names, nesting, null handling, timestamp
format); the constructed cases exercise the normalizer's enum mapping.

The fail-closed requirement (06 §5.20: any unrecognized status or conclusion maps
to `unknown`) is what makes this safe rather than a gap — an unseen-in-the-wild
value is handled by the same branch as a value GitHub adds after this capture.

## Refreshing

Re-capture with the same `gh api` calls listed above and update the version and
date in the table. Do not edit the JSON by hand for any reason.
