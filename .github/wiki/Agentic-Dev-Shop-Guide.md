# Agentic Dev Shop Guide

Operator-facing guide to the agent drivers that satisfy `pkg/provider.AgentProvider`
(each one under `providers/agents/<name>`) — how each lane is configured, what it
can and cannot do, and how an operator enables anything that ships disabled by
default.

## Agent Drivers

### Local Model Lane (`providers/agents/local`)

The local-model lane dispatches in-process over the `pkg/provider.ModelProvider`
seam — typically a locally-served Ollama instance — with no subprocess and no new
egress class. It sits in the same lane pool a plugin or vendor-hosted driver
occupies.

**Supported task classes.** `classify`, `extract` and `summarize` always dispatch,
regardless of which model is configured. `code`, `reason`, `review` and
`arbitrate` are gated on the `authoring` capability, which is resolved **per
model id**, never per driver.

**Authoring is disabled by default.** A freshly configured local lane advertises
no authoring capability for any model: there is no constructor option, config
setting, or environment variable that grants it. The only way a model id ever
gains `authoring` is a passing run of the named qualification fixture at
`providers/ollama/testdata/qualification/` against that specific model id. The
result is recorded in the `config` storage domain under
`agents.local.authoring_qualified.<model_id>` as `{passed, fixture_hash,
model_id, recorded_at}` — a TOML setting it is not; `08-INIT-CONFIG-SPEC.md` §3
gains no `[agents.local]` section from this.

**Enabling authoring for a model.**
1. Run the qualification fixture against the model id you intend to use. The
   fixture is a small, fixed set of prompt/expected-substring cases; a model
   passes only when every case passes.
2. The passing result is recorded against that exact model id. A different
   model id, or the same model id after the fixture itself changes (the
   recorded `fixture_hash` no longer matches the current fixture), reads as
   unqualified again — fail closed, not fail open.
3. Before relying on the qualified model for `code`/`reason`/`review`/
   `arbitrate` work, the operator should independently verify a sample of its
   output on real tasks — the fixture is a floor, not a substitute for that
   review.

**What cannot grant authoring.** No constructor parameter, no CLI flag, no
config file edit, and no environment variable can set a model id's authoring
capability outside a passing fixture run. `authoring`, and every capability this
driver advertises, is also on the automation-safety hard denylist so an
automated behavioral proposal can never flip it without an operator's explicit
elevation, once that denylist mechanism ships (Epic AI).

See `docs/cli/run.md` for the `cascade run --task` help text, generated from
`cmd/cascade/run.go`'s cobra Long/Example text, which states the same
task-class support inline at the CLI.
