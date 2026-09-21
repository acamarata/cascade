# Plugin Author Guide

How to write a cascade plugin: the manifest v2 schema, the three runtime
tiers, the two provider contracts a plugin can implement, the seven
host-ABI functions a WASM guest calls into, the process-plugin stdio
handshake, credential handling, network-scope declarations, lifecycle,
a worked example, and a troubleshooting FAQ.

This page extends the plugin surface documented elsewhere on this wiki
rather than repeating it: the process-plugin stdio JSON-RPC handshake flow
and the host boundary / credential custody design are owned by the pages
those tickets publish. Where this guide touches either topic it links out
instead of re-deriving them.

> **Source-of-truth note.** Every claim below was checked against the code
> in this repository as of this writing (`pkg/plugin`, `pkg/provider`,
> `internal/plugins/wasm`, `internal/plugins/process`). Two places where
> the original planning text and the shipped code disagree are called out
> explicitly, quoting both, rather than silently picking one.

## 1. Plugin manifest v2 schema

Every plugin ships a manifest that decodes into `pkg/plugin.Manifest`
(`pkg/plugin/manifest.go`). The schema field is a version tag, not free
text:

```go
const SchemaVersion = "cascade.plugin/v2"
```

Any other value in `schema` is a hard rejection (rule R1).

| Field | TOML key | Required | Notes |
|---|---|---|---|
| `ID` | `id` | yes | Must match `[a-z][a-z0-9-]*`. Becomes a storage namespace and a CLI/RPC mount point, so it is validated as a security boundary, not a style nit. May not claim the `plugin.__host__.*` namespace, which the host reserves for its own storage. |
| `Name` | `name` | yes | Human display name. |
| `Schema` | `schema` | yes | Must equal `cascade.plugin/v2` exactly. |
| `Version` | `version` | yes | The plugin's own semver. |
| `HostVersion` | `host_version` | yes | A semver *range* the host must satisfy: an optional comparator (`^`, `~`, `>=`, `<=`, `>`, `<`, `=`) plus a possibly-partial core, space-separated tokens AND together, or the bare wildcard `*`. **Known gaps, rejected rather than silently ignored:** npm-style `\|\|` alternation and `1.x`-style wildcard segments. |
| `Runtime` | `runtime` | yes | One of `builtin`, `process`, `wasm`, `remote` (§2). |
| `Provides` | `provides` | no | Intents, tools, storage domains, and CLI/RPC commands this plugin contributes (see below). |
| `Requires` | `requires` | no | **Capability names, not driver names** — the host grants capabilities, a plugin never names a concrete host driver it depends on. |
| `Permissions` | `permissions` | no | Consent-dialog entries (`name` + `description`) shown to the user before the plugin is enabled. |

`Provides` groups four lists, each optional:

- `intents []{name, description}` — natural-language capabilities the
  intent-install flow can resolve to this plugin.
- `tools []{name, description}` — agent-callable, function-calling tools.
- `domains []{name, description}` — storage domains the plugin registers
  (see §8 for scoping). A domain name may not claim
  `plugin.__host__.*` either.
- `commands []{name, description, rpc_method}` — CLI verbs the plugin
  mounts under its own namespace. `rpc_method` is optional: leaving it
  empty makes the command CLI-only, no RPC mount. A `commands[].name`
  colliding with a reserved core noun or utility verb from the CLI
  command tree is a hard rejection (rule R5).

Validation is fail-closed end to end: `ParseManifest` never returns a
`Manifest` alongside a non-nil error, and every rejection carries a
machine-readable `ErrCode` (`schema-version`, `unknown-runtime-mode`,
`required-field`, `malformed-version`, `command-name-collision`,
`invalid-capability-ref`) layered on top of the taxonomy `Kind` cascade
uses everywhere else.

A minimal manifest, built the way the shipped example plugin builds one
(`plugins/examples/example-builtin/plugin.go`):

```go
plugin.Manifest{
    ID:          "example-builtin",
    Name:        "Example Builtin",
    Schema:      plugin.SchemaVersion,
    Version:     "0.1.0",
    HostVersion: ">=0.1.0",
    Runtime:     plugin.RuntimeBuiltin,
    Provides: plugin.Provides{
        Tools:    []plugin.ToolSpec{{Name: "greet-tool", Description: "Greets the caller by name."}},
        Intents:  []plugin.IntentSpec{{Name: "greet-intent", Description: "Satisfies a natural-language greeting request."}},
        Commands: []plugin.CommandSpec{{Name: "greet", Description: "Print a greeting."}},
    },
}
```

A `process` or `wasm` plugin instead ships a TOML file on disk that
decodes into the same struct via `ParseManifest` (`pkg/plugin/loader.go`),
which uses strict decoding — an unrecognized top-level key is itself a
parse rejection (`ErrCodeParse`), not a silently ignored field.

### `provides.commands[].name` is the command PATH

`CommandSpec` has no path or segments field, so the nesting of a
plugin-contributed CLI verb comes from its name. The host splits the name into
segments, and those segments ARE the command path under `cascade`:

| `name` | mounts as |
|---|---|
| `github-repos` | `cascade github repos` |
| `github-ci-wait` | `cascade github ci wait` |
| `github.ci.merge-on-green` | `cascade github ci merge-on-green` |

The separator is `.` when the name contains one, and `-` otherwise. Use the
dot form whenever a segment must itself contain a hyphen: under the hyphen
rule, `github-ci-merge-on-green` would mount five levels deep
(`cascade github ci merge on green`), which is not what you meant. The dot
form is the same namespacing convention `provides.tools[].name` already uses.

Two further rules, both fail-closed:

- **An empty segment mounts nothing.** `-wait`, `github--wait` and
  `github..ci` cannot name a command path, so the host declines to guess one.
- **The first segment is your plugin's user-facing noun, and it must not
  collide with a host command.** It need not equal your manifest `id`
  (`cascade-github`'s noun is `github`). A plugin whose first segment collides
  with an existing `cascade` command does NOT shadow it: none of that
  plugin's verbs mount, and the plugin appears in `cascade --help` under its
  manifest `id` as a command that refuses with the reason. Rename the verb.

The mount lives in `cmd/cascade/plugin_process_mount.go` for process-tier
plugins and in `cmd/cascade/plugin_namespaces.go` (via
`BuiltinRegistry.NewCobraCommand`) for builtin ones.

## 2. Runtime tier selection

`Runtime` selects one of four `RuntimeMode` values:

| Mode | What it means | When to choose it |
|---|---|---|
| `builtin` | Go code compiled directly into the `cascade` binary, registered via `plugin.RegisterBuiltin` from an `init()`. | First-party plugins only — this is not available to a third-party author, since it requires a build of `cascade` itself. |
| `process` | A supervised child process the host execs, speaking stdio JSON-RPC 2.0 (§5). | Third-party plugins that need arbitrary host-language logic, filesystem or subprocess access, or an existing binary you do not want to recompile to WASM. |
| `wasm` | A `wazero`-hosted WebAssembly module, one fresh instance per dispatched call. | Third-party plugins that want sandboxing without an out-of-process supervisor, or a language that compiles cleanly to WASM. |
| `remote` | A remote endpoint reached over the network. | **Not implemented in the current release.** Remote runtime is planned for P2 and is gated behind the default-off `[plugins].enable_remote_runtime` flag; setting it does not make a working remote runtime appear, it only ungates the (currently absent) capability. Do not ship a plugin that declares `runtime = "remote"` expecting it to run today. |

**Trusted-tier gate (process plugins).** `internal/plugins/process` only
launches a manifest whose `TrustTier` is `trusted`; every other value,
including the zero value, is refused before anything execs. Because a
process plugin runs as an ordinary child process with no sandbox, the
host also renders an **unsandboxed-execution consent warning** listing
the plugin's declared `NetScopes` before the user can approve it —
`internal/plugins/process`'s own manifest type is what currently carries
`TrustTier` and `NetScopes` (see the callout in §7 for why these two
fields are not yet part of `pkg/plugin.Manifest` itself).

**WASM plugins** get sandboxing "for free" from `wazero`: no such trust
gate exists because there is no host-process access to escalate into.
Every call still gets a fresh module instance (`Dispatch` creates one per
call), so no two in-flight calls against the same plugin ever share
linear memory.

## 3. AgentProvider and ReviewProvider interfaces

A plugin's guest-side implementation binds into one of two host
contracts, both declared in `pkg/provider` (which imports nothing from
`internal/`, so a plugin author never needs an internal import to
implement either).

### `AgentProvider`

The exact same five-verb shape as the api-backed `ModelProvider` — from
the router's perspective a plugin-hosted agent and an api-backed model
driver are interchangeable dispatch targets:

```go
type AgentProvider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    Embed(ctx context.Context, req ModelEmbedRequest) (ModelEmbedResponse, error)
    Count(ctx context.Context, req CountRequest) (CountResponse, error)
    Stream(ctx context.Context, req ChatRequest, sink StreamSink) error
    Capabilities(ctx context.Context, lane string) (Capabilities, error)
}
```

- `Chat` completes a request as a single, non-streaming exchange.
- `Embed` returns one vector per input, in order.
- `Count` returns the plugin's own token count for a text string.
- `Stream` completes a request as a sequence of typed events delivered to
  `sink`, terminating in exactly one done or error event — never both,
  never neither.
- `Capabilities` describes the named lane's tool-capability support and
  compliance posture, the same descriptor `ModelProvider` publishes.

A manifest's `provides.tools` or `provides.intents` entry binds its
`Name` to one of exactly five `AgentProviderMethod` values (`chat`,
`embed`, `count`, `stream`, `capabilities` — `pkg/plugin/types.go`), so a
guest cannot register a typo'd or invented verb; there is no sixth verb.

### `ReviewProvider`

The contract a plugin providing CR-A/B/C review implements:

```go
type ReviewProvider interface {
    Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
    Capabilities(ctx context.Context, lane string) (Capabilities, error)
}
```

`ReviewRequest` carries `Level` (`CR-A`, `CR-B`, or `CR-C`), `Diff` (the
unified diff, or full contents for a new file), and free-text `Context`.
`ReviewResponse` carries every `ReviewFinding` (`Severity` one of `nit`,
`minor`, `major`, `blocker`; `File`; `Line`; `Message`) plus an overall
`Approved` bool. `Approved` must be `false` whenever any finding is
`ReviewSeverityBlocker`, but may be `true` alongside a non-empty
`Findings` list when every finding is a nit.

Error-return convention for both interfaces: a method returns a non-nil
`error` for anything the caller should treat as the call failing outright
(the request was not serviced) — a **substantive** finding (a review
blocker, an unsupported chat parameter) is reported through the method's
own result type, not as a Go error. Wrap host- or transport-level
failures (a crashed process, a malformed WASM response) with cascade's
taxonomy the same way a first-party driver does; a bare
`errors.New`/`fmt.Errorf` from a plugin's own host-side wiring is a
boundary-lint violation on this side of the boundary, and produces no
information the host can classify on the guest side either.

## 4. Host-ABI functions for WASM guests

> **Naming note.** Earlier planning text for this guide named these
> functions `host_http_request`, `host_storage_get`/`put`/`delete`/`list`
> (as four separate functions), `host_log`, `host_stream_emit`,
> `host_secret_ref`, `host_event_emit`, and `host_tool_register`. The
> shipped host-ABI v1 surface (`internal/plugins/wasm/doc.go`) exports
> exactly **seven** functions under the `env` wazero host-module name,
> with these real names: `host_http`, `host_storage` (one function, with
> an `op` field selecting among get/put/delete/list), `host_log`,
> `host_stream`, `host_secretref`, `host_eventemit`, `host_toolregister`.
> The count (seven) matches; four of the seven names do not. This section
> documents the real, shipped names, and calls out the discrepancy once
> per function below rather than silently picking one text over the
> other.

All seven guest calls share one wire shape: the guest JSON-encodes its
request into the module's shared linear-memory scratch region, calls the
host import with `(ptr, length)`, and the host function JSON-decodes it,
does the work, and writes back a `result{payload, error}` envelope (exactly
one of the two set) at a second fixed offset, returning its byte length.
Every host function fails closed on a malformed request — a bad
pointer/length pair or invalid JSON never panics the guest call, it comes
back as a structured `error` string in the result envelope.

### `host_http` (aka `host_http_request` in early planning text)

**The only WASM outbound network path.** Request: `{method, url, headers,
body}`. Response: `{status, headers, body}`. Every call is checked against
the calling plugin's declared net scopes *before* the delegate is ever
invoked — an empty scope list refuses every URL (fail closed), and a
scope is either a bare hostname (exact match) or `*.example.com` (suffix
match). A refusal returns `ErrNetScopeViolation`
(`KindPermissionDenied`). This is the policy chokepoint: nothing else in
the WASM host-ABI can originate a network request.

### `host_storage` (aka `host_storage_get`/`put`/`delete`/`list` in early planning text)

One function, not four. Request: `{op, key, value}` where `op` is one of
`get`, `put`, `delete`, `list`. Response: `{value, keys}` — `value` is set
only for `get`, `keys` only for `list`. Backed by `pkg/plugin.Storage`,
scoped per plugin (§8).

### `host_log`

Request: `{level, message}`. Response: `{accepted}`. Routes one structured
log line through the host's `Logger` delegate. Useful for debug output
during development — see the troubleshooting section (§10).

### `host_stream` (aka `host_stream_emit` in early planning text)

Request: `{channelId, data}`. Response: `{bytesWritten}`. Writes a chunk to
a named streaming output channel.

### `host_secretref` (aka `host_secret_ref` in early planning text)

Request: `{name}`. Response: `{refId}`. See §6 — this is the *only* way a
plugin obtains anything credential-shaped, and the response type has no
field capable of carrying a literal secret value even if a broken broker
implementation tried to return one through it.

### `host_eventemit` (aka `host_event_emit` in early planning text)

Request: `{topic, payload}`. Response: `{eventId}`. Publishes to the
plugin event bus.

### `host_toolregister` (aka `host_tool_register` in early planning text)

Request: `{toolName, schema}`. Response: `{registered}`. Registers a tool
with the host tool registry at runtime, distinct from the static
`provides.tools` manifest declaration in §1.

A `nil` host-side dependency reaching any of the seven functions (the
composition root wired that seam incompletely) returns a typed
`KindInternal` "dependency not configured" result rather than a nil-panic
— every one of the seven fails this way, not just some of them.

## 5. Process plugin stdio JSON-RPC handshake

Full detail on the frame format, transport, and version-negotiation
exchange lives on the page the process-plugin ticket (O/S-31.T3)
publishes; this section is the author-facing summary an implementer needs
to get a `process`-tier plugin talking to the host at all.

On launch, before any ordinary call is accepted, the host sends a
`cascade.hello` notification carrying the minimum protocol version it
accepts (`min_protocol_version`, default `1.0.0` — `PluginProtocolVersion`
in `internal/plugins/process/types.go`, overridable per manifest). The
plugin must respond with `cascade.hello_ack`, carrying its own
`protocol_version` and a `manifest_hash`. Versions compare as strict
`major.minor.patch` triples — a pre-release suffix or a missing component
does not parse, and an unparseable version on either side compares as
*lower* than any well-formed one (fails closed, never admitted by
default). If the plugin's version is below the host's minimum, the host
sends `cascade.version_mismatch` (best-effort — the process is about to
be terminated regardless of whether the notification lands) and refuses
the handshake.

Ordinary calls after a successful handshake use plain JSON-RPC 2.0 framing
over the same stdio transport: `{jsonrpc, id, method, params}` requests,
`{jsonrpc, id, result | error}` responses (exactly one of `result`/`error`
set), and `{jsonrpc, method, params}` notifications carry no id and expect
no response.

**Crash isolation and restart policy.** A health-monitor goroutine watches
the child process's exit. On an unexpected exit it restarts with
exponential backoff — `InitialBackoff * 2^(attempt-1)` — up to
`RestartPolicy.MaxAttempts` (default 3, default initial backoff 1s). Once
the attempt budget is exhausted, the handle transitions to a permanent
invalid state and every subsequent call fails fast with
`ErrPluginUnavailable` rather than attempting another launch. Backoff
waits are driven off a context-derived timer, never a bare `time.Sleep`,
so a shutdown request interrupts a pending restart immediately.

## 6. Credential and secret access

**Broker-only, via `host_secretref` (§4). Never in manifest literals or
config files.** A plugin never receives a raw secret value inside the
WASM host-ABI: `SecretRefRequest{name}` returns `SecretRefResponse{refId}`
— an opaque, short-lived reference token the broker itself resolves back
to a value only at the point of actual use, under its own scoped
authority. `SecretRefResponse` is structurally incapable of carrying a
literal: it has exactly one field, and that field is the reference id,
not a value.

The security rationale (§5.21): a manifest or config file is written to
disk, may be committed to a plugin's own repository by mistake, and is
readable by anything with filesystem access to the plugin's install
directory. A short-lived, scope-bound reference token that only the
broker can redeem is not subject to any of those exposures — losing the
token loses nothing a fresh call could not re-request through the same
consented channel, and it cannot be replayed outside the scope the
broker issued it for. **Any plugin manifest, TOML config, or source file
that embeds what looks like an actual API key, password, or private key
literal is a defect, not a convenience** — route it through
`host_secretref` (WASM) or the equivalent broker call on the process-tier
transport instead.

## 7. Net-scope declaration pattern

> **Contract-vs-tree callout.** Planning text for this guide expected
> network scopes to be a field of the manifest schema itself
> (`pkg/plugin.Manifest`). As shipped, `pkg/plugin.Manifest` carries no
> `net_scopes` or `trust_tier` field at all — only `Requires` (capability
> names) and `Permissions` (consent-dialog entries). `NetScopes` and
> `TrustTier` are currently declared on `internal/plugins/process.Manifest`,
> a process-runtime-local type (`internal/plugins/process/types.go`
> documents this explicitly as a recorded contract deviation, not an
> oversight). Until a future ticket folds net-scope declaration into the
> canonical `pkg/plugin.Manifest` schema, a process-tier plugin author
> declares net scopes through the process-runtime manifest surface, not
> the TOML `provides`/`requires` fields described in §1.

A scope is either a bare hostname (`api.example.com`, exact match) or a
wildcard suffix (`*.example.com`, matching any subdomain but never the
bare `example.com` itself). An empty scope list is a fail-closed refusal
of every outbound URL — there is no "no scopes declared means unrestricted"
reading anywhere in this design. The **trusted-tier gate** (§2) and the
declared net scopes are shown together in the unsandboxed-execution
consent warning before a `process`-tier plugin is ever launched, and
`host_http` (§4) enforces the same scope list on every WASM-tier call —
one declaration, two enforcement points.

A future **per-plugin net-scope audit** (listing every plugin's declared
scopes and whether each has actually been exercised) is anticipated by
this design but is not part of the current release; nothing in the tree
today implements it.

## 8. Lifecycle hooks

`pkg/plugin.Storage` scopes each plugin's storage to its own declared
`provides.domains` entries — reaching outside a declared domain requires
an explicit capability grant, not an implicit one. `Migration` and
`MigrationStep` (`pkg/plugin/storage.go`) carry a plugin's schema
migrations for its own storage domain forward across versions; a plugin
author registers migrations alongside domain declarations rather than
hand-rolling schema changes.

The full enable/disable/remove/update lifecycle — including
**grant-expansion permission-diff re-confirmation** (re-prompting consent
when an updated manifest requests broader permissions than the version
the user originally approved) and **rollback on a failed handshake**
(reverting an in-progress update if the new version's `cascade.hello_ack`
exchange, §5, never completes) — is a host-side policy this guide
describes at the design level because **no `cascade plugin
enable/disable/remove/update` CLI surface exists in the tree as of this
writing.** What does exist today and that a plugin author can rely on:

- The process-runtime's own crash-isolated restart (§5) is the closest
  shipped analog to "rollback on failure" — a plugin that never completes
  its handshake within `StartupTimeout` is treated as failed, not
  silently left half-initialized.
- `internal/rpc/elevation.go` already treats
  `plugins.set_enable_remote_runtime` and `plugins.enable`/`.disable` as
  **elevated** operations requiring an explicit approval step — so even
  before the lifecycle CLI itself lands, the elevation policy an author
  should expect around enabling a plugin is already decided.

Do not build a plugin that assumes an update flow can silently widen its
own permission set without a re-confirmation step; treat every version
bump that adds a `requires` or `permissions` entry as needing the same
consent flow a fresh install would.

## 9. Worked example

> **Contract-vs-tree callout.** This guide is written to reference
> `plugins/examples/example-domain/`, the storage-domain worked example
> from P1-E15-W4-S33-T2. As of this writing that path does not yet exist
> in the tree — T2 has not landed. The closest currently-real analog is
> `plugins/examples/example-builtin/` (§1's manifest snippet is drawn
> from it), a **builtin**-tier plugin, not a storage-domain worked
> example. Once `plugins/examples/example-domain/` lands, this section
> should be updated to reference its actual manifest fields and
> host-ABI calls by file path and line, per this guide's own accuracy
> standard; until then, the callouts below describe what the worked
> example is expected to demonstrate rather than asserting it exists.

`example-domain` (pending) is expected to demonstrate a `wasm`-tier
plugin that declares one `provides.domains` entry and exercises
`host_storage`'s four operations (`get`/`put`/`delete`/`list`, §4)
against that domain — the natural pairing of §1's manifest `domains`
field with §4's storage host-fn and §8's storage-scoping model. Until it
lands, `plugins/examples/example-builtin/plugin.go` remains the concrete,
buildable reference for manifest construction (§1) and the
`BuiltinHandlers` shape a `builtin`-tier plugin implements — it does not
demonstrate WASM host-ABI calls or storage-domain scoping, since a
builtin plugin runs in-process and needs neither.

## 10. Troubleshooting / FAQ

**"My manifest is rejected with `schema-version`."** Your `schema` field
is not exactly `cascade.plugin/v2` (§1, rule R1). Check for a stray
version suffix or a copy-pasted older schema string.

**"My manifest is rejected with `required-field`."** Either `id` or
`name` is empty, or `id` does not match `[a-z][a-z0-9-]*` — no uppercase,
no leading digit, no underscore.

**"My manifest is rejected with `command-name-collision`."** A
`provides.commands[].name` (or, separately, an `id` or
`provides.domains[].name`) collides with a name the host reserves —
either a core CLI noun/utility verb, or the `plugin.__host__.*` namespace
reserved for host-owned storage. Rename the colliding entry; there is no
override.

**"My `host_version` range is rejected even though it looks like valid
semver."** Two range forms are documented known gaps, not bugs: npm-style
`\|\|` alternation (`"1.0.0 || 2.0.0"`) and `1.x`-style wildcard segments.
Use space-separated AND'd comparators instead (`">=1.0.0 <2.0.0"`), or the
bare `*` wildcard if any host version is acceptable.

**"WASM toolchain setup."** A guest module must export a
`cascade_abi_version` i32 global equal to `HostABIVersionV1` (currently
`1`, `internal/plugins/wasm/doc.go`). `Load` refuses any module whose
global is present and does not match — there is no silent degradation to
an older ABI. If your toolchain does not let you export a plain i32
global by that name, check its documentation for global-export support
before assuming the ABI check itself is broken.

**"My process plugin never gets past launch."** Check the handshake
(§5) first: does your `cascade.hello_ack` response include a
`protocol_version` that parses as strict `major.minor.patch`? A
pre-release suffix (`1.0.0-beta`) does not parse under this host's
comparator and will be treated as lower than any well-formed version,
which fails the handshake even if your actual version is newer than the
host's minimum.

**"How do I debug a WASM plugin without a debugger attached?"** Use
`host_log` (§4) liberally during development — every call is a real,
synchronous round trip to the host's `Logger`, so log lines appear in
order relative to your guest's own execution, unlike a fire-and-forget
event.

**"Can I make outbound HTTP calls from a WASM plugin some other way?"**
No. `host_http` (§4) is the only network egress point in the WASM
host-ABI, by design — it is the one place net-scope enforcement (§7) and
the host's egress substitution/sensitivity pass can intercept every
outbound byte. There is no second path, sanctioned or otherwise.

**"Does `runtime = \"remote\"` work yet?"** No. Remote runtime is planned
for P2 and is not available in the current release, regardless of the
`[plugins].enable_remote_runtime` flag's value — see §2.

## 11. Process-tier host boundary enforcement

Every process-tier host-ABI call — `host_http_request`, `host_storage_*`,
`host_secret_ref`, and every other capability-gated call — transits
`internal/plugins/host.HostBoundaryEnforcer` before anything else happens
with it. This is the process-tier counterpart to §4's WASM host-ABI
checks (`host_http`'s inline net-scope check, `host_secretref`'s broker
routing): same rules, same fail-closed posture, a separate implementation
because the two runtimes cannot share code across the
`plugins-providers-boundary` package boundary.

**What gets checked.**

- `host_http_request` — the target URL's host against your declared net
  scopes (§7's bare-hostname / `*.example.com` rules apply identically).
- `host_storage_*` — the domain against your declared storage domains;
  a domain outside that set is refused unless you hold the explicit
  cross-domain capability.
- `host_secret_ref` — routed to the vault broker's reference verb; you
  receive an opaque handle, never the stored value (§6's rules apply
  identically on this tier).
- everything else (`host_tool_register`, `host_event_emit`,
  `host_stream_emit`) — the policy engine's own allow/ask/deny decision
  for that capability. An "ask" verdict is treated as a denial at this
  boundary: a host call has no channel back to an operator to answer an
  interactive prompt mid-call.

**Denied-call error structure.** Every refusal is `cascade`'s
capability-denied error kind, wrapping the specific reason: an
out-of-scope host for HTTP, an undeclared domain for storage, the
broker's own error for a secret reference, or the policy engine's
explanation text for everything else. Every denial is also recorded to
the audit trail with your plugin id, the call type, and that same reason
— nothing about a refusal is silent.

**Known gap.** The process-tier transport does not yet carry a result
back to a plugin for a request-shaped host call — an allowed
`host_http_request` is checked and would proceed, but nothing yet
delivers its HTTP response to your process. This is a transport-layer
gap tracked in `docs/security-posture.md` §Plugin host boundary
enforcement, not a plugin-author-facing behavior change: a denied call
still behaves as documented above.

## 12. Intent resolution

An intent is a named capability your plugin declares under
`provides.intents` (§1). When the host has an intent string in hand and no
plugin bound to it yet, it asks the intent resolver which plugin should
satisfy it. This section describes what the resolver does with your
declarations, so you can predict whether your plugin will be chosen.

**Installed first.** The resolver scans the installed plugins before it
looks at the registry at all. A plugin is considered only if the host has
it enabled: a disabled plugin is never a candidate, whatever its manifest
declares. The match is on the intent name, compared case-insensitively
after trimming surrounding whitespace, so `plan-phase` and `PLAN-Phase`
select the same plugin. Exactly one enabled installed match ends the
lookup, and the registry is not consulted.

**Registry tiers.** If no installed plugin matches, the resolver ranks the
entries in the verified registry index. The registry index publishes an
id, a name, a description, and free-text tags for each plugin; it does not
publish `provides.intents`. Tags are therefore how you make an entry
findable by intent: publish the intent name as a tag. Three tiers, in
order of strength:

1. **Exact tag.** One of the entry's tags equals the intent.
2. **Tag prefix.** One of the entry's tags has the intent as a prefix.
   The intent `lint` matches the tag `linter`. The direction is one way
   only: the tag `lint` does not match the intent `linter`.
3. **Keyword.** The intent appears somewhere inside the entry's name or
   description.

A stronger tier always wins outright, and candidates within one tier are
ordered by plugin id so the result is the same on every call. The entry's
id is deliberately not matched at any tier. An id that happens to read
like an intent is a naming coincidence, not a declaration, so name the
intent in your tags if you want the entry to surface for it. Weaker
matches are kept in the result behind the winner rather than discarded, so
a caller can show alternatives.

**Ambiguity is refused, not guessed.** If two or more candidates tie at
the strongest tier, the resolver returns an ambiguity carrying the whole
ranked list plus the size of the tied group, and it installs nothing. The
caller decides how to ask the user which one to use. Nothing is silently
dropped to break a tie, and no plugin is picked just because it sorted
first. Two enabled installed plugins declaring the same intent is the same
situation and gets the same answer.

**A registry candidate needs a verified index.** The resolver will not
read an index it cannot prove was signed. It accepts only a verified-index
witness, a value that can be produced only by running the real signature
verification over the raw index bytes; there is no way to build one around
that check. With no witness the resolver refuses outright rather than
returning a partial answer. In practice this means a tampered or unsigned
index yields no candidates at all, and your plugin can be resolved from
the registry only while the index carrying it verifies. Installed-first
resolution is unaffected, because it never reads the index.

**A blank intent is a refusal.** An empty or whitespace-only intent string
is rejected as invalid input, which is a different answer from "nothing
provides this intent". A caller that cannot tell the two apart is a caller
with a bug, so the resolver keeps them separate.
