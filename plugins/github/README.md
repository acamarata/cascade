# cascade-github

A process-tier Cascade plugin: an OAuth broker plus tool groups for GitHub
repositories, issues and pull requests.

It is launched as its own binary from `manifest.toml` by the host's
ProcessRuntime and speaks newline-delimited JSON-RPC over stdin/stdout. It is
never registered in the compile-time builtin registry — registering it there
would give it a second identity that skips the trust gate a process-tier
install goes through. Two tests hold that line: `main_test.go` asserts that
linking this package registers nothing, and
`internal/plugins/builtin_tier_only_test.go` asserts the registry carries no
`cascade-github` entry where that registry is actually populated.

## Tools

Every tool is namespaced `cascade-github.*`. The plugin **performs its own
calls** against the GitHub API and returns decoded results. That direct
egress is the declared, accepted-risk design for a trusted-tier plugin: the
manifest names exactly one host, `api.github.com`, and the consent warning
shown at install time says so. Every URL is assembled from a fixed base plus
a validated path — never from a caller's input or a response field — and
redirects are not followed, so nothing can move a call to another host or
another repository.

| Tool | Request it builds |
|---|---|
| `repos.list` | `GET /users/{owner}/repos` |
| `repos.get` | `GET /repos/{owner}/{repo}` |
| `repos.clone_url` | reads the clone URL off a fetched repository record |
| `issues.list` | `GET /repos/{owner}/{repo}/issues` |
| `issues.get` | `GET /repos/{owner}/{repo}/issues/{number}` |
| `issues.create` | `POST /repos/{owner}/{repo}/issues` |
| `issues.comment` | `POST /repos/{owner}/{repo}/issues/{number}/comments` |
| `issues.close` | `PATCH /repos/{owner}/{repo}/issues/{number}` |
| `prs.list` | `GET /repos/{owner}/{repo}/pulls` |
| `prs.get` | `GET /repos/{owner}/{repo}/pulls/{number}` |
| `prs.create` | `POST /repos/{owner}/{repo}/pulls` |
| `prs.merge` | `PUT /repos/{owner}/{repo}/pulls/{number}/merge` |
| `prs.review_request` | `POST /repos/{owner}/{repo}/pulls/{number}/requested_reviewers` |

`api.github.com` is the only host this plugin reaches, declared in the
manifest as `net.http:api.github.com`.

## Authorization

The plugin runs an OAuth authorization-code flow with PKCE and asks for the
classic **`repo`** scope. The verifier never leaves the process; only the
challenge goes on the wire. The resulting token is handed to Cascade's vault
through a `host_secret_ref` notification and is **never written to a config
file or to the manifest**.

The flow is two calls:

- `cascade.auth.begin` binds an ephemeral port on `127.0.0.1`, returns the
  authorization URL to open, and keeps the verifier in memory. It refuses
  immediately when `CASCADE_NO_INPUT` is set: a flow that needs a browser
  cannot be completed by a process that may not prompt, and returning an
  authorization URL nobody can open would look like success.
- `cascade.auth.complete` waits up to five minutes for the redirect,
  verifies it against the flow's state, redeems the code, and emits the
  `host_secret_ref` notification. The token is not in the reply — a reply is
  correlated and recorded by the host, and the vault is the only place a
  token belongs.

A failed exchange stores nothing. GitHub answers a failed exchange with HTTP
200 and an `error` member rather than an error status, so the response is
validated before anything is stored; otherwise a non-token would be written
to the vault and fail one call at a time afterwards.

### Using a fine-grained token instead

If you would rather grant a fine-grained personal access token than run the
OAuth flow, the classic `repo` scope maps onto these repository permissions.
Grant only the rows for the tools you intend to use.

| Tool | Fine-grained permission | Access |
|---|---|---|
| `repos.list`, `repos.get`, `repos.clone_url` | Metadata | Read |
| `issues.list`, `issues.get` | Issues | Read |
| `issues.create`, `issues.comment`, `issues.close` | Issues | Read and write |
| `prs.list`, `prs.get` | Pull requests | Read |
| `prs.create`, `prs.review_request` | Pull requests | Read and write |
| `prs.merge` | **Contents** | Read and write |

Three things about this table are worth stating rather than leaving to be
rediscovered:

- **Merging is a Contents write, not a Pull requests write.** `prs.merge` is
  the one row whose permission does not match its tool group: merging writes
  commits to the base branch, so GitHub gates it on `Contents`. A token
  granted `Pull requests: Read and write` and nothing else can open and
  review a pull request but cannot merge one.
- **Metadata read is mandatory** on every fine-grained token, so the
  repository rows need no grant beyond the default.
- **`issues.list` returns pull requests too** (see below), and seeing those
  entries needs `Pull requests: Read` in addition to `Issues: Read`.

Source: GitHub's "Permissions required for fine-grained personal access
tokens" reference, checked 2026-09-14 against the exact endpoints in the
table above.

## Two external-contract findings

Both are pinned by tests against real `gh` 2.78.0 captures in `testdata/`,
and both would otherwise be silent wrong answers rather than errors.

**`/issues` returns pull requests.** GitHub's REST API treats every pull
request as an issue, so the issues endpoints return both, distinguished only
by the presence of a `pull_request` member. Code that does not know this
reports every pull request as an issue and counts both wrong.
`Issue.IsPullRequest()` exists for this, and
`TestIssuesEndpointReturnsPullRequestsToo` cross-checks the decoder against
the raw JSON so the decoder and the assertion cannot share a mistake.
GitHub documents the behavior on each issues endpoint.

**The pull-request LIST response carries no `merged` boolean.** The
single-PR response has `merged`; the list response has only `merged_at`. Code
reading `Merged` alone therefore reports every merged PR in a list as
unmerged. `PullRequest.IsMerged()` reads both, and
`TestListedPullRequestsCarryNoMergedBoolean` asserts the absence against the
real capture — so if GitHub ever starts sending the boolean in lists, the
test fails and the fallback gets revisited, which is the right outcome.

## Fixtures

`testdata/fixtures/` holds real responses captured with `gh` 2.78.0, not
hand-written shapes. `testdata/README.md` records their provenance, the exact
redaction rules applied, and what was deliberately **not** changed. Redaction
is parent-aware: `name` and `email` are blanked only inside objects that
carry a `login` (that is, inside user objects), so a repository's own `name`
survives.

## CI policy

`cipolicy` (`plugins/github/cipolicy`) holds this plugin's CI provider
policy resolver: the decision about where a repository's CI coverage
belongs. It is the only part of the never-pay rule that lives here.

| Repository | Route |
|---|---|
| matches a `[ci.policy.repos] private` glob pattern | local gate, with a refusal for the Actions path |
| no match, a GitHub token configured | hosted GitHub Actions |
| no match, no token configured | local gate |

Patterns are globs over `owner/repo` (`path.Match`, case-insensitive), so
`acamarata/*` covers an owner and `*` never crosses the `/`. There is no
`allow_paid` key and no bypass parameter: `Config` carries the pattern list
and a boolean for token presence, and nothing else, so no caller can force
a private repository onto paid CI.

The resolver is a pure decision — no network call, no subprocess, no clock
— and it imports `pkg/cascade` and the standard library only.

**The run and status verbs are not here.** Per R-16.31 `cascade ci run` and
`cascade ci status` are core commands (`internal/ci`), not plugin verbs; see
`docs/cli-reference/ci.md`. The host reads this resolver's decision through
the composition bridge at `internal/plugins/ci_policy_wiring.go`, which is
the one package allowed to import both `internal/**` and `plugins/**`
(Art.10.2). That bridge is also why the resolver sits in the `cipolicy`
subpackage rather than in `plugins/github` itself: `plugins/github` is
`package main`, and nothing can import a main package.

What this plugin still owns for CI is ingestion: normalizing GitHub Actions
runs into the `ci_results` domain. That path now asks the same resolver
before it spends a request, and refuses to poll Actions for a repository the
policy routes local.

## Build and test

```bash
go build ./plugins/github
```

```bash
go test ./plugins/github/...
```

The default test lane never opens a socket: the HTTP transport, the token
poster and the callback listener are injected seams, and Art.7.2 forbids an
untagged test from importing `net` or `net/http` at all. The code that does
open one — `HTTPDoer.Do`, `HTTPPoster` and the loopback listener — is
covered by the integration lane instead, against a real local server:

```bash
go test -tags integration ./plugins/github/...
```

The plugin imports `pkg/**` and its own packages only, never `internal/**`
(Art.10.2).
