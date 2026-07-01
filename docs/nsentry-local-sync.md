# nSentry local sync — daemon-owned observability pipeline

Cascade owns the developer-machine side of nSentry: every project's box error
reports, GitHub Actions failures, and Dependabot alerts/PRs land in that
project's `.claude/inbox` automatically, deduped per developer, with no
hand-made launchd agents or scripts. The `cascade-daemon` runs it from one
declarative config.

## How it works

Three report streams per project, each on its own schedule, all delivering into
`<project>/.claude/inbox`:

| Stream | Source | Default cadence | Mechanism |
|---|---|---|---|
| `rsync` | box `/opt/nself-ops/errors/*.md` | 5 min | rsync over SSH → inbox, per-dev `consumed.list` dedup |
| `ci` | that org's GitHub Actions failures | 15 min | `gh` → Markdown reports → inbox, `.gh-seen` dedup |
| `dependabot` | org Dependabot alerts + version-update PRs | 6 h | `gh` → Markdown reports → inbox, `.dependabot-seen` dedup |

The daemon loads `~/.cascade/nsentry-sync.yaml` on start and schedules each
enabled `(project, stream)` job. Jobs are keyed by `(project, stream)`, so a
config reload reconciles rather than duplicating. Per-dev rsync dedup state lives
at `~/.cascade/nsentry/<dev_id>/<project>/consumed.list` (out of the project
tree); the two GitHub bridges keep their seen-manifests in the inbox
(`.gh-seen`, `.dependabot-seen`).

Bundled bridge scripts (`crates/cascade-daemon/assets/nsentry/`) are written to
`~/.cascade/nsentry/scripts/` at daemon start and invoked with per-project args.
`gh`, `rsync`, and `bash` are resolved by absolute path (the daemon runs with a
minimal PATH).

## Config schema

`~/.cascade/nsentry-sync.yaml`:

```yaml
version: 1
projects:
  - name: nself                                   # unique key
    path: /Users/you/Sites/nself
    org: nself-org                                # GitHub org for the ci/dependabot bridges
    sentry_server: root@sentry-errors.nself.org   # rsync source (user@host)
    remote_dir: /opt/nself-ops/errors             # optional, this default
    inbox: /Users/you/Sites/nself/.claude/inbox
    enabled: true                                 # project master switch
    per_run_cap: 50                               # max reports one stream run delivers (flood guard)
    streams:
      rsync:      { interval_secs: 300,   enabled: true }
      ci:         { interval_secs: 900,   enabled: true, per_repo: 10 }
      dependabot: { interval_secs: 21600, enabled: true }
```

No secrets in this file. rsync uses your SSH key (`accept-new`); the bridges use
your authenticated `gh` CLI.

## Reading status

```
cascade nsentry status
```

Shows, per project and stream: enabled, last run (relative), reports delivered
(last run / total), and any error. A stalled stream is obvious — an old
`last-run` or a non-empty `error` column. Status is persisted to
`~/.cascade/nsentry-sync-status.json` after every run, so it survives restarts.

Other commands:

```
cascade nsentry list                       # configured projects + schedules
cascade nsentry run [--project P] [--stream rsync|ci|dependabot]   # trigger now
cascade nsentry pause  --project P         # set enabled=false in the yaml
cascade nsentry resume --project P         # set enabled=true
```

`run` executes the same code the daemon schedules, so it is the way to verify a
project end to end.

## Adding a 5th project

Append a block to `projects:` in `~/.cascade/nsentry-sync.yaml` (name, path, org,
sentry_server, inbox, streams). Ensure your SSH key is authorized on the new box
(`ssh root@<box>` once to accept the host key) and your `gh` account can read the
org. The daemon reconciles on its next tick; `cascade nsentry run --project <new>`
verifies it immediately.

## Pausing a project

`cascade nsentry pause --project <name>` (or set `enabled: false`). All three of
that project's streams stop; re-enable with `resume`.

## Safety

- A consumed report is never re-delivered (per-dev manifest + bridge seen-lists).
- Reports are only ever written inside the configured `inbox`.
- If a sentry box is unreachable (or its SSH host key changed — `accept-new`
  refuses a *changed* key; run `ssh-keygen -R <host>` once after a box is
  reprovisioned), the run logs the error, records it in status, and continues —
  other projects and streams are unaffected.
- `per_run_cap` bounds how many reports a single run delivers, so a CI "fixing
  storm" or a large backlog can't flood an inbox in one shot; the remainder syncs
  on the next run.

## Notes

- Org-level Dependabot **alerts** need a `gh` token with the org's Dependabot
  read grant; without it the alerts call returns 404 and is skipped gracefully —
  the Dependabot **PR** stream still works.
- This replaces the earlier hand-made launchd bootstrap
  (`org.nself.sentry-sync`, `org.nself.gh-ci-bridge`,
  `org.nself.dependabot-bridge`), which is fully removed.
