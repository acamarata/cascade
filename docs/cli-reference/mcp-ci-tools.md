# CI tools for MCP clients

`cascade mcp serve` offers two tools over the same local gate that
`cascade ci run` and `cascade ci status` use. Both call the same code as the
commands, so a run started from a tool is recorded exactly like a run started
from a terminal.

| Tool | Method | Capability | Effect |
|---|---|---|---|
| `cascade_ci_run` | `ci.run` | `ci.run` | runs the configured lint, test and build steps |
| `cascade_ci_status` | `ci.status` | `ci.read` | lists recent CI results (read only) |

Neither tool has a v1 ancestor. They are served by the `mcp serve` process
itself. They are not daemon RPC methods.

## Availability

A tool is listed only when the policy engine allows its capability for the
MCP subject. A policy that does not allow it hides the tool from `tools/list`
entirely, and `cascade mcp tools list` reports it as withheld.

`ci.read` is a read-class capability. `ci.run` is a local-development class
capability: it executes the commands you configured under `[ci.local]` on this
machine.

## `cascade_ci_run`

Parameters (all optional, all booleans; an unknown field is refused as
invalid input):

| Parameter | Meaning |
|---|---|
| `lint_only` | run only the lint step |
| `test_only` | run only the test step |
| `build_only` | run only the build step |

There is deliberately no repository, path or command parameter. The gate runs
in the working directory the `mcp serve` process was started in, and only runs
the steps from `[ci.local]` (or the defaults detected for that checkout).

Result:

```json
{
  "run_id": -1,
  "passed": false,
  "failed_step": "test",
  "steps": [
    { "name": "lint", "exit_code": 0 },
    { "name": "test", "exit_code": 1 }
  ]
}
```

A failing step is a result (`passed` is `false`), not an error. What a step
printed is never returned; read it in the terminal by running `cascade ci run`.
`failed_step` is an empty string when every step passed.

Refusals, all before any step runs and before the results database is touched
by the run:

| Situation | Error kind |
|---|---|
| unknown or mistyped parameter | invalid input |
| the working directory is not a repository checkout | invalid input |
| the never-pay policy routes the repository to hosted GitHub Actions | policy denied |
| no never-pay policy resolver is installed | unavailable |

Both methods are owner-only. A process started with a caller principal token
never runs the gate or reads results in-process: it forwards the call to the
daemon, which serves no `ci.*` method, so the call is refused.

### Shared store

`cascade mcp serve` holds the embedded store's write lock for its own recall
index whenever that index opens, and a running daemon holds it too.
`cascade ci run` refuses when that lock is held. The MCP method does not: the
run, its jobs and its steps are still recorded in the results database and
show up in `cascade_ci_status`, but the best-effort step journal entries and
the run-completed event are skipped, so a `[ci.watch]` subscriber is not
notified of that run. Only a refusal because the lock is held takes this path;
any other store failure refuses the run before a step runs. These tools are
only reachable once the policy grants `ci.run` / `ci.read`; until then they
stay withheld from `tools/list`, so this path applies to a served, granted
tool and not to a default install.

### One run at a time

A `cascade_ci_run` call made while another `cascade_ci_run` is still running
in the same `mcp serve` process is refused (conflict, "a run is already in
progress") and starts nothing, so two builds never run over each other in one
checkout from this server. `cascade_ci_status` is never blocked by it. The
limit covers one server process; a second process, or `cascade ci run` in a
terminal, is a separate run.

### What runs

The steps are the ones under `[ci.local]`. When none are configured, the
commands detected for the checkout (for example `go test` for a Go module) are
used, exactly as `cascade ci run` does. An agent that calls `cascade_ci_run`
in a checkout it does not trust therefore executes that checkout's detected
build and test commands, so grant `ci.run` only for repositories you would run
yourself.

## `cascade_ci_status`

| Parameter | Meaning |
|---|---|
| `limit` | runs to return, 1 to 50. Defaults to 10. Outside the range is refused as invalid input. |

Result: `{"runs": [...]}`, newest first, GitHub Actions and local runs
together. Each run carries `run_id`, `repo_id`, `name`, `status`, `conclusion`,
`source` (`github-actions` or `local`) and `updated_at` (RFC 3339, UTC).
