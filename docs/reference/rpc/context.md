# Context RPC methods

Methods for assembling, generating and repairing repository context. Access
classes and elevation are listed in the generated RPC method reference.

### `context.scope.show`

Resolves the session scope for a working directory.

Params (all optional): `cwd`, `user`, `machine`, `branch`, `task`, `session`
and `explicit_overrides`. Malformed params are `invalid-input`.

Result: the resolved session scope, with fields such as `kind`, `user`,
`machine`, `cwd`, `workspace`, `product`, `project`, `repository`,
`package_path` and `branch`.

### `context.show`

Shows the merged instruction tiers found for a working directory.

Params: `cwd`.

Result: `tiers`, a list of `{name, ordinal, content, tokens}`. `name` is the
section role, followed by its heading when it has one. Token counts are a
naive estimate.

### `context.slice`

Assembles the context slice for a working directory under a token budget.

Params: `cwd`, optional `max_tokens` (default 8000) and optional
`session_id`.

Result: `tier`, `retrieval`, `memory`, `counts`, `dropped`, and when present
`directives` and `inbox`. `inbox` lists the envelopes drained for the given
session, each with `id`, `class`, `origin_scope`, `data_class`, `trust`,
`summary`, the `target_*` fields and `visibility`.

### `context.sync`

Regenerates the harness instruction files for a working directory, or only
checks them for drift.

Params: `cwd` (required, non-empty) and optional `check_only`.

Result: `drift` and `files` (both omitted when empty), `regenerated` and
`already_fresh`. With `check_only` set, nothing is written. When the sync
fails the call returns the error.

### `context.harness_list`

Lists the harnesses this machine has installed and whether their generated
instruction file has drifted.

Params: `cwd` (required, non-empty; `invalid-input` otherwise).

Result: `harnesses`, a list of `{kind, detected, install_path,
instruction_path, ...}`. `instruction_path` is empty for a harness that was
not detected. Drift is reported when it can be computed.

### `context.harness_sync`

Same params, result and behaviour as `context.sync`; it is registered with the
harness handlers.

### `context.generate`

Generates the managed context files for a repository, or checks them.

Params: `repo` (an absolute path equal to the repository's git top-level
directory) and `check`. Anything else is `invalid-input`. The repository is
registered first.

Result: with `check` true, `{"drift": ...}` listing the managed files that
differ from the manifest; nothing is written. Otherwise the generation
result: `Written`, `Adopted`, `Refused` and `Deferral` (untagged field
names). A refused file adds an error item to the supervision queue; when that
queue is unavailable the call is `unavailable`.

### `context.facts.list`

Lists every stored fact version for a repository.

Params: `repo` (the repository path).

Result: `facts`. An unregistered repository gives an empty list.

### `context.facts.propose`

Asks the extract model for repository facts the detectors do not cover and
records them as proposals.

Params: `repo`. Only a few tracked files are read (manifests, README and CI
workflow files), each capped at 32 KiB and 128 KiB in total; files the
admission check refuses are counted.

Result: `proposed`, `accepted`, `rejected` (lists of facts) and `refused`
(the count of refused files). It is `unavailable` when the model door is not
ready.

### `context.facts.accept`

Accepts the newest proposed version of a fact. It always needs a verified
elevation for exactly this call; otherwise `elevation-required`.

Params: `id`.

Result: `fact`, the accepted fact.

### `context.hydration.degraded`

Records that context hydration ran degraded, by publishing an event.

Params: `reason` (required, non-empty; `invalid-input` otherwise).

Result: `seq`, the event sequence number. The method is registered only when
the event bus and clock are wired.

### `context.hydration.report`

Counts the degraded-hydration events in a recent window.

Params: optional `window_seconds`; zero or absent uses the default window.

Result: `count` and `window_seconds`.

### `context.repair`

Audits the repository's context indexes through the repository watcher and
repairs them.

Params: `repo` (absolute), `rebuild` and `reason`. With `rebuild` true,
`reason` must be 1 to 200 characters and the indexes are discarded and
rebuilt after an audit event is appended; without it, `reason` must be empty.
Violations are `invalid-input`.

Result: `{"repaired": ...}`. It is `unavailable` when the watcher or the
audit appender is not available.
