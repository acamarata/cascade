# nSentry — Bug/CI/Error Report Sync

nSentry pulls `*.md` report files from a remote ops server into your project's
`.claude/inbox/` (the Claude Code / AI inbox) exactly once per developer. It uses rsync over SSH plus a
per-developer consumed manifest to prevent duplicate delivery across a shared
server.

---

## Quick start

```bash
cascade sentry enable --server ops@your-server.example
```

This writes `.cascade/nsentry.toml` and (on macOS) installs a launchd agent
that runs `cascade sentry sync` on the configured interval.

---

## How it works

1. rsync copies `*.md` files from `<server>:<remote_dir>/` into a per-project
   local cache at `~/.cascade/nsentry/<dev_id>/<project>/cache/`.
2. Each file not yet listed in that project's
   `~/.cascade/nsentry/<dev_id>/<project>/consumed.list` is copied into the
   project inbox.
3. Newly delivered filenames are appended to that project's `consumed.list`.

This is idempotent. Re-running sync after files are already delivered produces
zero new inbox entries. Multiple developers sharing the same server each have an
independent consumed list, so each receives every report once. State is keyed by
**both** developer and project, so two projects synced on one machine keep
separate caches and manifests — a report from one project's server can never
land in another project's inbox.

**dev_id** — a stable 12-character hex identifier derived from your hostname
and username. It is computed locally; nothing is sent to any external service.
If you replace a machine, your new dev_id will be different and you will receive
all reports again (correct behavior — the manifest tracks per-machine delivery).

---

## Config file

Location: `<project_root>/.cascade/nsentry.toml`

```toml
sentry_server = "ops@your-server.example"
remote_dir    = "/opt/nself-ops/errors"   # default
interval_secs = 120                        # default (launchd StartInterval)
```

`sentry_server` is the only required field. All values are user-supplied; no
server address is hardcoded in Cascade.

An optional `inbox` field overrides the default inbox path:

```toml
inbox = "/path/to/custom/inbox"
```

---

## Commands

### `cascade sentry enable`

Write config and install the sync agent.

```
cascade sentry enable --server ops@host.example \
  [--remote-dir /opt/nself-ops/errors] \
  [--inbox .claude/inbox] \
  [--interval 120] \
  [--project /path/to/project]
```

On macOS, installs a launchd agent at
`~/Library/LaunchAgents/dev.cascade.nsentry-<project-slug>.plist` that
runs `cascade sentry sync` at the configured interval and at login.

On Linux and other platforms, the config is written but no scheduling agent is
installed. Add `cascade sentry sync --project <dir>` to a systemd --user timer
or cron manually.

### `cascade sentry sync`

Run one sync immediately.

```
cascade sentry sync [--project DIR] [--dry-run]
```

`--dry-run` prints what would be copied without writing any files.

### `cascade sentry status`

Show sync state for known projects.

```
cascade sentry status [--project DIR ...]
```

Output includes the server, inbox path, manifest size (number of files already
consumed), and whether the launchd agent is loaded.

### `cascade sentry disable`

Remove the launchd agent and delete `.cascade/nsentry.toml`.

```
cascade sentry disable [--project DIR]
```

The project's `consumed.list` manifest in `~/.cascade/nsentry/<dev_id>/<project>/`
is not removed, so re-enabling after disable will not re-deliver already-consumed
reports.

### `cascade sentry update`

Regenerate the launchd plist from the current config (idempotent). Use this
after updating the `cascade` binary so the plist points to the new binary path,
or after changing `interval_secs` in `nsentry.toml`.

```
cascade sentry update [--project DIR]
```

---

## State directory

```
~/.cascade/nsentry/
  <dev_id>/
    <project>/
      cache/           # rsync mirror of this project's remote directory
      consumed.list    # one filename per line, never shrinks
```

The cache and manifest are never written into the project — they stay in your
home directory, out of version control.

---

## Servers are user-supplied

Cascade ships no default server address. You choose and operate the ops server.
The remote directory default `/opt/nself-ops/errors` matches the nSelf ops
convention but can be overridden with `--remote-dir` at enable time or by
editing `nsentry.toml` and running `cascade sentry update`.
