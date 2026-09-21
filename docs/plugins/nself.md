# cascade-nself

Package: `plugins/nself` · Manifest id: `cascade-nself` · Runtime: `builtin`

Reports whether a directory is an nself-managed project and whether the
`nself` CLI is reachable. It never becomes a core dependency: `internal/`
and `pkg/` import zero symbols from `plugins/nself` except the one
composition root that wires it (`internal/plugins/nself_wiring.go`).

**This plugin does less than its ticket described, deliberately.** The two
`nself` verbs the contract was written against do not exist in the CLI it
was built against, and the host seam it was meant to write config through
does not exist either. Both absences were probed, not assumed; both are
recorded in `plugins/nself/testdata/README.md`, and the plugin refuses
rather than pretending.

| Contract said | Reality (nself v1.3.5, probed read-only 2026-09-21) |
|---|---|
| detect with `nself project status --json` | no `project` verb: `unknown command "project"` |
| handshake with `nself add cascade` | `nself add <name>` installs a registry plugin; no JSON, no server profile |
| write the profile through `C-S05.T8` config verbs | `internal/runtime/config_write.go` validates dotted paths; no diff-apply seam exists |

> **Runtime-tier note.** The plugin was specced as `runtime = "process"`,
> `trust_tier = "trusted"`. Neither field exists in the real
> `cascade.plugin/v2` schema, and no composition root in this tree can
> launch a process-tier manifest today. T0 ruled `runtime = "builtin"` as
> the floor. That is a security downgrade, stated rather than glossed: the
> plugin runs in the daemon process with full host trust and no supervised
> child. `plugin.go`'s package doc quotes both sides.

## Tools

| Tool | Behaviour |
|---|---|
| `nself_project_info` | answers the detection question; never fails because a directory turned out not to be a project |
| `nself_add_cascade` | returns ONE typed refusal (`KindUnsupported`) naming both missing prerequisites, and attempts nothing |

## Detection heuristic

1. **Marker scan.** Walk from the scan root upward. A directory counts as a
   project when it holds a `.nself` **directory** that itself holds one of
   the files nself writes: `build-version`, `compose-files.txt`,
   `build-state`, `.first-run-complete` or `nself.yml`. The file set comes
   from real projects on a real machine (listed in `testdata/README.md`).
   - `nself.toml` is **not** a marker. No such file exists in any real
     nself project; an earlier draft invented it.
   - A `.nself` directory holding something else (a `pipelines/`
     directory, say) is not a project.
   - `$HOME/.nself` is nself's own global state directory and is refused by
     path, before any content check. Without that rule every directory
     under the home directory reads as a project.
2. **Bounds.** The walk stops at the home directory (exclusive) and at the
   first repository root (`.git`) it examines, whichever comes first, with a
   hard 64-level cap behind both. It never runs to `/` from a directory
   inside your home.
3. **Subprocess probe** (only on a marker-scan miss). Runs
   `nself status --json` — the one project-scoped verb with a real JSON
   mode — **in the scanned directory** (`cmd.Dir`), with a closed
   environment allowlist, a 2-second deadline, its own process group, and a
   group kill on the deadline. Exit 0 means detected.
4. **Every probe failure means "not detected", never an error.** A binary
   absent from `PATH`, a timeout, and a probe that *ran and exited
   non-zero* (what v1.3.5 does in any non-project directory: `no nself
   project found…`, exit 1) all fold to `detected: false`. The typed
   outcome is reported on the doctor leg as a code-chosen classification
   (`binary-absent`, `timed-out`, `ran-and-failed`, `not-runnable`).

The result is memoized per detector for a daemon session; the memo is
cleared after a config reload.

**Disclosed limit.** `nself status` exits 2 when a real project's services
are unhealthy, so the subprocess leg reports "not detected" there. The
marker scan is the primary signal and is unaffected.

## What a response can contain

```json
{
  "detected": false,
  "method": "none",
  "doctor": {
    "binary_reachable": true,
    "binary_path": "/opt/homebrew/bin/nself",
    "probe_outcome": "ran-and-failed"
  }
}
```

No byte of the subprocess's own stdout or stderr ever reaches a response:
`nself status` prints service state that can name a database URL. Two
independent layers keep it that way — the payload is BUILT from code-chosen
constants, a boolean, a directory path and this plugin's own error text;
and every string field is then passed through a credential scrub that
replaces anything shaped like a URL with userinfo or a secret-named
key/value pair. The scrub runs whatever the egress firewall would have
done.

## Config fields written

**None.** There is no config write at this floor, because there is no
handshake to write and no apply seam to write through. When a config
diff-apply verb lands, the ticket that builds it wires its own caller.

## Windows

The plugin builds and its tests compile for `GOOS=windows`. Binary absence
returns a typed doctor error with a remediation hint naming the platform,
never a panic (`TestNselfPlugin_WindowsRefusal`, which injects both the
platform and the `PATH` lookup so it proves the same thing on every
machine). On Windows the probe's deadline kills the child but cannot reap a
whole process tree — a typed refusal says so rather than claiming otherwise
(`exec_windows.go`).

## Egress

Every response transits the `nself-backend` egress class
(`internal/hooks/egress/classes.go`, owner `P1-E25-W5-S52-T2`) at tier
`internal`. `internal/plugins/nself_wiring.go` binds the REAL
`egress.Engine` to the plugin's local interceptor seam, and the adapter
refuses any class other than `nself-backend`. With nothing bound — a build
without that wiring, or an operator who disabled the class — the plugin
**refuses to emit** rather than passing bytes through unfiltered.

## Undone documentation item

The ticket's second docs item ("Plugin author guide: reference cascade-nself
as the canonical example of soft-default wiring via the config-write API")
is **not landed**, and cannot be: there is no soft-default config wiring
here to be canonical about. It stays open for the ticket that builds the
config-apply seam.
