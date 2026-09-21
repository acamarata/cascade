# `cascade ci`

Runs the local lint/test/build gate, and shows what CI has recorded.

`ci` is a core noun. It is not a verb of the `cascade-github` plugin, and
it needs no daemon: both verbs run in embedded mode against the local
`cascade.db`, fully non-interactive, with no prompts.

```
cascade ci run [flags]
cascade ci status
```

## `cascade ci run`

Runs the configured steps in a fixed order — lint, then test, then build —
stopping at the first one that fails, and records the result.

| Flag | Meaning |
|---|---|
| `--repo` | repository root to run in (default: the current directory) |
| `--lint-only` | run only the lint step |
| `--test-only` | run only the test step |
| `--build-only` | run only the build step |

Exit status is zero when every step that ran passed, non-zero otherwise.
The failing step's name and exit code are in the output, and the whole run
is written to the `ci_results` domain with `source=local`, so
`cascade ci status` and any wait-on-green consumer see it.

`--repo` must be an existing directory holding a `.git` entry (a directory
in a normal clone, a file in a linked worktree or submodule). A path that
is not a checkout is refused before any step runs: otherwise a typo runs
the operator's commands somewhere unintended and files the result under
the wrong repository.

### Routing: which repositories may run locally

Before anything runs, `ci run` asks the never-pay policy where this
repository's CI belongs. A repository the policy routes to hosted GitHub
Actions is refused:

```
$ cascade ci run
error: policy denied: ci: acamarata/cascade routes to github-actions per
[ci.policy]; its CI runs on hosted GitHub Actions, not the local gate
```

The repository is named from its own `origin` remote. A checkout with no
`origin` — or one whose remote is not a recognisable `owner/repo` — runs
locally: there is no hosted CI for it to be routed to.

If no policy resolver is configured at all, both verbs refuse with
`unavailable` rather than assuming the local gate is fine. A missing policy
is missing infrastructure, and a never-pay rule must not guess.

## `cascade ci status`

Shows the most recent runs, newest first, from **both** producers: rows the
GitHub Actions ingestion wrote (`source=github-actions`) and rows the local
gate wrote (`source=local`). The `source` column is what distinguishes
them; local run ids are additionally negative, so the two producers' id
spaces cannot collide.

It takes no flags. It is a read of the local database — it polls nothing
and spends nothing, so there is no routing decision to make here. The
refusals live where money is at stake: `ci run` above, and the Actions
polling path, which refuses to spend a request on a repository the policy
routes local.

## Configuration

### `[ci.policy]`

```toml
[ci.policy.repos]
private = ["acamarata/*", "someone/one-repo"]
```

`private` is a list of **glob patterns** over the `owner/repo` string
(`path.Match` semantics, case-insensitive). `acamarata/*` covers every
repository under that owner; `*` does not cross the `/`, so it never
claims another owner's repositories. An unparseable pattern is a
config-load error naming the offending key, never a silently skipped entry.

Routing rules:

| Repository | Route |
|---|---|
| matches a `private` pattern | local gate; the Actions path is refused |
| no match, a GitHub token configured | hosted GitHub Actions |
| no match, no token configured | local gate |

There is no `allow_paid` key and no bypass flag. Never-pay is enforced by
the shape of the configuration, not by an option an operator could set to
defeat it.

A "GitHub token configured" means the `cascade-github` plugin's OAuth token
is in the vault.

### `[ci.local]`

```toml
[ci.local]
lint = ["golangci-lint run ./..."]
test = ["go test -race ./..."]
build = ["go build ./..."]
env = ["MY_BUILD_FLAG"]
timeout_seconds = 600
```

| Key | Meaning |
|---|---|
| `lint`, `test`, `build` | command strings for that phase; a phase may list more than one |
| `env` | extra environment-variable **names** to pass through to every step |
| `timeout_seconds` | per-step timeout (default 300) |

Any of `lint`/`test`/`build` left unset is filled from the repository type:
a `go.mod` at the root gives the three Go commands above. A repository type
with no rule and no explicit commands is an error — the gate never
silently runs nothing.

Each command is run by the platform shell (`sh -c` on macOS and Linux,
`cmd /C` on Windows) with the repository root as its working directory.

### The step environment

A step does **not** inherit the environment `cascade` was started with. It
receives a built one: the variables below when they are set, plus
`CI=true`, plus any names `[ci.local] env` lists.

`PATH` · `HOME` · `TMPDIR` · `LANG` · `LC_*` · `GOPATH` · `GOFLAGS` ·
`GOCACHE` · `GOMODCACHE`

Everything else is absent, `GITHUB_TOKEN`, `AWS_*`, `NPM_TOKEN` and every
`CASCADE_*` setting included. A step is an operator-configured command
running with cascade's own privileges, and handing it the ambient
environment would hand it every credential that happens to be in the
shell. `env` widens the list by **name** — the value comes from the
environment, never from `config.toml`, so this key never puts a secret in
a config file. `CI=true` cannot be overridden.

### Timeout semantics

Each step gets `timeout_seconds` of its own. On expiry:

- **macOS, Linux** — the step runs in its own process group and the whole
  group is killed, so a command that backgrounded work (`make test &`, a
  dev server, a forking harness) does not leave survivors behind.
- **Windows** — the step's shell is started as its own process group
  leader and is killed, but there is no portable whole-tree reap here, so
  the result carries a typed warning saying a backgrounded grandchild may
  still be running. That limitation is reported, not hidden.

On both platforms the run itself is bounded: a surviving process holding
the step's output pipes cannot keep the command waiting past its deadline.

A timed-out step is a failed step — the sequence halts, the run is
recorded with a `timed_out` conclusion for that step, and the exit status
is non-zero.
