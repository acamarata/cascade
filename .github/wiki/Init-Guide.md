# Setting Cascade Up

```
cascade init
```

walks nine steps and leaves the machine configured. Every step that
finishes is written to a journal, so a run that is interrupted picks up
where it stopped rather than asking again.

## The nine steps

| | Step | What it does |
|---|---|---|
| 1 | preflight | Reports what is already here and proves the cascade home can be written to, before asking anything. |
| 2 | profile | `local`, `server` or `worker`. A worker hands off to `cascade node enroll` and stops — a worker is configured by its controller. |
| 3 | storage | Local: confirm the SQLite path. Server: Postgres, Redis and S3, as vault references. |
| 4 | plugins | A checklist of the plugins this build registers. |
| 5 | providers | Runs `cascade provider add` for each one you add. |
| 6 | harnesses | Detects the supported harnesses and wires the ones you confirm: instruction files, hook pack, MCP entry. |
| 7 | telemetry | Opt-in. The default is no, in every mode. |
| 8 | daemon | Installs the daemon as a platform service and enrolls the elevation helper's key. |
| 9 | doctor | Runs the first-run health check and prints a summary. |

## Running it without a terminal

```
cascade init --yes
```

accepts every default and asks nothing: local profile, the catalog's
defaults, every detected harness wired, telemetry off, daemon installed.
This is the path automation uses.

It configures **no providers**. Adding one needs a credential or a
browser, and a mode whose contract is "ask nothing" can obtain neither.
Run `cascade provider add` afterwards.

```
cascade init --check
```

reports what a run would change and writes nothing at all — not the
journal, not config, not a harness file. It exits 0 when the machine is
already set up and 3 when there is work to do, which makes it usable as a
CI assertion.

## Driving it from a file

```
cascade init --config cascade-init.toml
```

reads every answer from a `cascade.init/v1` file. `CASCADE_INIT_CONFIG`
is the same instruction.

```toml
schema = "cascade.init/v1"
profile = "local"

[plugins]
enable = ["cascade-claude", "pbd"]

[[providers]]
name = "anthropic"
auth = "key-env"
key_env = "ANTHROPIC_API_KEY"
verify = true

[harnesses]
detect = true
install = ["claude"]

[telemetry]
enabled = false

[daemon]
install = true
```

**`key_env` names a variable, never a key.** The value is read from your
environment at the moment the provider is added and handed straight to
`cascade provider add`; it is never written to the file, the journal, or
a log line. A setup file is a file people commit, so a key written into
one is refused before any step runs — and the refusal does not quote what
it found.

`auth = "oauth"` is refused on this path. The browser flow needs a person
at the browser, and a file-driven run has neither; use `key-env`, or run
`cascade init` interactively.

An unknown key is refused with a suggestion rather than ignored:

```
unknown key "plugins.enabel" in the setup file; did you mean "plugins.enable"?
```

A file declaring a schema this build does not read is refused too, rather
than read for whichever fields happen to be recognised.

### Precedence

Flags beat the environment, which beats the file, which beats the
defaults. One exception, in one direction: `CASCADE_TELEMETRY=0` wins over
a file that enabled telemetry, and nothing can enable it over an
environment that said 0.

### Never blocking

`CASCADE_NO_INPUT=1` turns any question that would block into an error
naming what was missing. `--yes` and a complete setup file both satisfy
it — the guard fires only where nothing can answer and nothing may be
assumed.

## Flags

| Flag | Effect |
|---|---|
| `--yes` | Accept every default, ask nothing. |
| `--check` | Report the diff, write nothing, exit 3 if there is work. |
| `--profile <p>` | Preselect step 2. |
| `--harness a,b` | Wire only these, from the ones detected. |
| `--no-daemon` | Skip step 8. |
| `--config <f>` | Read every answer from a `cascade.init/v1` file. |
| `--reconverge` | Converge an existing install toward the file, keeping your edits. |
| `--force-section <s>` | During a reconverge, overwrite your edits in these config sections. |

`--harness` narrows what was **detected**. Naming a harness this build
does not know is refused rather than quietly wiring nothing — if you
asked for it to be set up, a successful run that set nothing up is the
wrong answer.

If both `--yes` and `--check` are given, `--check` wins. They disagree
about whether to write, and the safe reading of a contradictory
instruction is the one that changes nothing.

## What happens when a run is interrupted

Each completed step is written to `init-state.json` in the cascade home,
along with what that step decided — the profile chosen, the providers
added, the harnesses wired. Re-running resumes at the first step that did
not finish. Nothing is asked twice.

A run that completes deletes the journal. Its presence is the signal that
a run was interrupted, so leaving one behind would make the next run
report a resume with nothing to resume.

The journal holds provider **names** and no credential of any kind. It is
a plaintext file in your cascade home.

If it was written by a newer version of Cascade, or if it cannot be read,
`init` refuses rather than starting over. Treating an unreadable journal
as "nothing has happened yet" would re-run steps that already changed the
machine.

## Windows

Step 8 is skipped there with a message: Windows has no service manager
this build installs into, and the elevation helper is not enrolled
because elevation is refused on that tier. Init still completes and exits
0 — a platform where a step does not apply has not had setup go wrong.
Run `cascade daemon run` yourself, or start it from Task Scheduler.

## Converging an existing install

```
cascade init --config cascade-init.toml --reconverge
```

brings a machine that is already set up back in line with the file. It
converges; it never regenerates.

**Your edits win.** A config value you changed is yours: the run reports
the difference and leaves your value in place. `--force-section
<section>` overrides that for one section, and only that section.

```
kept your retrieval.fusion = off (this run wanted on; --force-section retrieval to override)
```

The same rule everywhere else:

- **Providers** already in the registry are re-verified, never re-added,
  and a provider the file does not mention is never removed.
- **Plugins** you turned on yourself are left on, even if the file lists
  them under `disable`.
- **Instruction files** are regenerated only when they are stale *and*
  untouched. One you hand-edited is reported and left alone.

It exits 0 when everything converged and 3 when your edits held. A key the
file asks for that this build's configuration does not have is reported
as unsettable rather than attempted.

`--reconverge` needs `--config`: without a file there is nothing to
converge toward but the shipped defaults, and comparing against those
would try to revert every deliberate choice on the machine.

## See also

- [Harness Guide](Harness-Guide.md) — what step 6 set up, and how to keep
  it current.
