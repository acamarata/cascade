# The in-process conductor door

`internal/daemon/conductor_door.go` (P1-CPA-10) is how code inside the daemon
reaches the model layer: embeddings, the classifier and reply calls. It is
the provider of `contract:conductor-door-inprocess`. There is one door per
process, and it has no socket face: the daemon registry serves no
`conductor.embed` method, so no client can declare an embedding tier.

## Getting the door

```go
door := daemon.ProcessConductorDoor() // the same pointer on every call, never nil
if err := door.Ready(); err != nil {
    // not bound yet, or the daemon's Executor failed to build
}
```

`RegisterConductorExecuteHandler` binds the door, inside its own body, to
the one `*conductor.Executor` it builds (or to the reason it has none). Its
signature is unchanged. `bind` is unexported, so only `internal/daemon` can
bind, and `internal/build` `TestArchOneConductorExecuteRegistration` keeps
production to one `RegisterConductorExecuteHandler` caller
(`cmd/cascade/daemon_unix_conductor.go`) and one `bind` caller
(`internal/daemon/conductor_execute.go`). A second daemon composition in the
same process (tests do this) rebinds the door to its own Executor.

| Door state | Every method returns |
|---|---|
| never bound | `KindUnavailable` "conductor door not bound" |
| bound after a construction failure | `KindUnavailable` wrapping the construction error |
| bound to a constructed Executor | the Executor's own answer |

## Methods

| Method | Rides | Refuses before dispatch |
|---|---|---|
| `Embed(ctx, lane, model, tier, inputs)` | `Executor.Embed` (the `model.embed` door) | a tier other than internal or public (`KindPolicyDenied`, one audit record); a lane name the registry does not hold (`KindNotFound`) |
| `Execute(ctx, req)` | `Executor.Execute` (the `model.execute` door) | everything the Executor refuses: classifier, tier, `[conductor].deny_task_classes`, `[conductor.quota]` |
| `ExecuteStream(ctx, req)` | `Executor.ExecuteStream` | the same as `Execute` |

The door satisfies `provider.ModelExecutor`.

### Embed

- The tier is an explicit argument. The caller computes it from daemon-side
  state (the source record's own tier), and the door never defaults it
  upward. Local-only, restricted and any invalid tier are refused before
  the lane lookup, with zero provider calls and one `policy.route` audit
  record (`Action` `model.embed`, `RiskLevel` the refused tier, the lane and
  model only as a hash).
- The lane resolves by an exact `LaneName` match from the registry's
  `ListLanes` into `provider.Selection{LaneID, Provider, Model}`. No alias,
  prefix or fallback lane is tried.
- The dispatch is `exec.Embed(conductor.ContextWithDoorSensitivity(ctx, tier), sel, provider.ModelEmbedRequest{Inputs, Model})`,
  so the P1-SEC-01 chokepoint (`enterDoor`) sees the tier, admits the lane
  for it, runs policy and substitutes the payload, and writes the call's
  audit record.

### Execute and ExecuteStream

The caller's ctx passes unchanged into the Executor and on to
`Router.Select`: the `principal.Caller`, any other ctx value and its
cancellation all survive. In-process callers such as a relayed node job
(running under a node-job principal and a `conductor.RelayOrigin`) rely on
this. The Executor's authorize path applies the live owner deny list and
the quota policy (P1-CAP-07), so a denied task class or an exhausted ceiling
returns that refusal with zero provider calls.

## What is deliberately absent

- No `conductor.embed` or any other door RPC on the daemon socket
  (`cmd/cascade` `TestConductorEmbedNotOnSocket`).
- No second Executor: the door holds the one the daemon composition built.
- No dispatch beside the P1-SEC-01 chokepoint: every door method calls a
  public Executor door verb.

## Tests

| Test | Proves |
|---|---|
| `TestConductorDoorUnboundUnavailable`, `TestConductorDoorExecutorUnavailable` | the two unready states |
| `TestConductorDoorRefusesLocalOnlyTier`, `TestConductorDoorRefusesRestrictedTier` | the tier refusal before dispatch, with one audit record |
| `TestConductorDoorUnknownLaneNotFound` | exact lane resolution |
| `TestConductorDoorEmbedDispatchesThroughExecutor` | one dispatch through `Executor.Embed` at the door tier |
| `TestConductorDoorExecuteHonoursOwnerDeny`, `TestConductorDoorExecuteHonoursQuota` | the P1-CAP-07 refusals on both execute verbs |
| `TestConductorDoorRebindReplacesExecutor` | a second composition rebinds |
| `TestDoorPreservesCallerContext` | the caller's principal, values and cancellation reach `Router.Select` |
