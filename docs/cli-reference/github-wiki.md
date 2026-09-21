# `cascade github wiki`

Push a repository's `.github/wiki/` directory to its GitHub wiki, and check
for drift between them. Both verbs are `github`-noun, plugin-contributed
commands (like `cascade github ci wait`, see `github-ci.md`) — the actual
git clone/commit/push runs inside the `cascade-github` process plugin
(`plugins/github/wiki`), not host-side core code.

```
cascade github wiki sync [--owner <owner>] [--repo <repo>] [--local-dir <path>] [--yes]
cascade github wiki check [--owner <owner>] [--repo <repo>] [--local-dir <path>]
```

> **Status.** Both verbs are mounted (`cmd/cascade/github_wiki_cmd.go`,
> `cmd/cascade/plugin_process_mount.go`) and their underlying logic
> (`plugins/github/wiki.Sync`/`CheckDrift`) is implemented, tested and
> reachable by RPC dispatch inside the `cascade-github` process
> (`wiki_cmd.go`'s `wikiReply`). What is still missing is the SAME
> prerequisite `cascade github ci merge-on-green` already names
> (P1-E25-W5-S51-T3): this build has no trust-elevation path that can
> launch a process-tier plugin yet, so both verbs refuse today with a
> typed `KindUnavailable` error naming that gap
> (`internal/plugins/wiki_wiring.go`'s `NewGitHubWikiCallerForHost`) —
> never a fabricated success. `cascade github wiki --help` and `sync
> --help`/`check --help` all work today; only the live git operation
> itself is blocked, on the elevated `plugin add` install flow
> (P1-E24-W5-S50-T4).

## Authentication

The token comes from `cascade-github`'s own vault-stored OAuth token
(the same one `cascade github repos/issues/prs` use) when this process was
interactively authorized. A CI-launched process never runs that flow, so
it also accepts `CASCADE_GITHUB_TOKEN`, `GITHUB_TOKEN` or `GH_TOKEN` from
its own environment, in that order (the identical precedence
`cascade github ci wait` already uses) — this is what lets the auto-sync
workflow below authenticate. The token never appears in a `git` argv, in
git config written to disk, or in any error/log message: it travels only
as a per-invocation `git` `http.extraheader` environment variable (the
same technique `actions/checkout` uses), never in the clone/push URL.

## Symlink refusal

A symlink anywhere under `--local-dir` (at any depth) refuses the entire
sync/check with a typed `KindPolicyDenied` error rather than being
followed: `git clone`/`push` only ever sees regular files this package
itself read, never a link's target, which could point outside
`.github/wiki/` entirely.

## Scope: public repositories only

GitHub does not expose a wiki git endpoint (`https://github.com/{owner}/
{repo}.wiki.git`) for a private repository under standard OAuth scopes.
Both commands check repository visibility first (the same `repos.get` call
`cascade github repos` already makes) and refuse with a typed,
actionable error on a private repository — no new API surface or OAuth
scope is added for the check.

## `cascade github wiki sync`

1. Reads `--local-dir` (default `.github/wiki`). An empty directory is a
   no-op: exit 0, nothing cloned or pushed.
2. Refuses if the repository is private.
3. Requires `--yes` (the process plugin itself has no terminal — its
   stdin/stdout carry the JSON-RPC channel to the host, so `--yes` is the
   confirmation, not an interactive prompt). Under `CASCADE_NO_INPUT=1`
   without `--yes`, it refuses immediately with "no prompt was attempted"
   rather than hanging.
4. Refuses if `git` is not on `PATH` (an actionable, typed error, not a
   panic, on every platform including Windows tier-2).
5. Clones the wiki to an isolated temp directory, overwrites it from
   `--local-dir` (adds/updates/removes files to match exactly), commits as
   `cascade-bot`, and pushes. Content that already matches the remote is a
   no-op — no empty commit is ever created.
6. A non-fast-forward push (someone else edited the wiki between this
   sync's clone and its push) refuses with guidance pointing at `cascade
   github wiki check`, never a silent overwrite or a force-push.

## `cascade github wiki check`

Read-only: clones the remote wiki to a temp directory (never pushes) and
diffs it against `--local-dir`, reporting three sorted lists — files added
locally, files removed locally (present only on the remote), and files
present in both with different content. Exits non-zero when any list is
non-empty, zero when all three are empty ("no drift"). Never asks for
confirmation and ignores `CASCADE_NO_INPUT` entirely: there is nothing in
this command's own options to confirm.

Designed to run as a CI check step ahead of merging a `.github/wiki/`
change, or on a schedule to catch manual edits made directly on the GitHub
wiki UI that the tracked directory has not picked up.

## Auto-sync workflow (push-to-main)

`internal/repo/templates/wiki-sync-workflow.yml` is a compiled-in GitHub
Actions workflow (R-21.187: workflow bodies come only from
`internal/repo/templates/`) that triggers on push to `main`, filtered to
`.github/wiki/**`, and runs `cascade github wiki sync --yes`. A repository
opts in by copying the template into `.github/workflows/` — this repo's own
`.github/wiki/` is the source of truth for cascade's public docs
(AA/S-55.T2), and adopts it the same way.

## Fixture provenance (Art.2)

`plugins/github/wiki/testdata/fixtures/hijri-core-wiki.bundle` is a real
git bundle captured from the public `acamarata/hijri-core` GitHub wiki with
the real `git` binary — never a self-authored dialect. Tool, version, date
and source repo are recorded in `plugins/github/wiki/testdata/README.md`.
No unit test in `plugins/github/wiki` reaches `github.com`: the sync/drift
pipeline is proven end to end against real local git remotes
(`git init --bare`) or this bundle, both entirely local.
