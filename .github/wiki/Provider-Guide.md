# Provider author guide

How to implement a new model provider driver and register it with the
conductor fleet. Everything here is derived from the merged `pkg/provider`
(driver contract) and `internal/providers` (intake, registry, health,
usage) implementations — no scope is invented on this page.

For the OAuth broker's own contract (PKCE flow, token storage, refresh and
revocation), see [docs/provider-guide.md](../../docs/provider-guide.md);
this page's Section 3 covers only when each credential form is offered and
how a driver reaches that broker, not the broker's internals.

## 1. The `ModelProvider` interface

Every driver (`providers/anthropic`, `providers/openai`, `providers/gemini`,
`providers/ollama`, and any future one) implements exactly five methods,
declared in `pkg/provider/types.go`:

```go
type ModelProvider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    Embed(ctx context.Context, req ModelEmbedRequest) (ModelEmbedResponse, error)
    Count(ctx context.Context, req CountRequest) (CountResponse, error)
    Stream(ctx context.Context, req ChatRequest, sink StreamSink) error
    Capabilities(ctx context.Context, lane string) (Capabilities, error)
}
```

- **`Chat`** completes a single, non-streaming exchange.
- **`Embed`** returns one vector per input string, in order.
- **`Count`** returns the provider's own tokenizer count for a piece of
  text (contrast the universal rune-based approximation elsewhere in the
  package — this is the provider's real count).
- **`Stream`** completes a chat exchange as a sequence of typed events
  delivered to a `StreamSink`, terminating in exactly one done or error
  event.
- **`Capabilities`** describes one lane's current tool-capability support
  and compliance posture; capabilities are resolvable per lane because
  pool members' tiers can differ per key.

The interface is vendor-neutral and auth-free by design: no
provider-specific type or credential shape crosses this boundary. A
runnable example lives in `pkg/provider/example_test.go` —
`go test -run Example ./pkg/provider/...` exercises it directly.

## 2. Shape-probe order

`cascade provider add` does not ask the operator which driver kind a
credential belongs to. It probes, in this fixed order:

1. **anthropic-compat**
2. **openai-compat**
3. **gemini**

The first endpoint that accepts the credential's shape wins; the driver
kind is set from whichever probe succeeded, never from a name the operator
typed. If all three probes fail, intake returns a structured error naming
every endpoint tried and the status (or dial failure) each one returned —
never a generic "invalid credential" message that discards the diagnostic
detail.

A caller who already knows the driver kind (for example, a non-interactive
`[[providers]]` intake directive) can set a driver hint to skip straight to
that probe; the hint still has to succeed on its own — a hint is never a
bypass of verification.

## 3. Key vs OAuth intake

`cascade provider add <name>` accepts exactly one credential source:

- **`--key`** reads a value from stdin. A credential is never accepted as
  a positional argument or a flag value — only stdin or a named
  environment variable ever carry one.
- **`--key-env <VAR>`** reads the named environment variable once,
  non-interactively. This is the form every automated or headless setup
  uses.
- **`--oauth`** runs the PKCE loopback flow described in
  [docs/provider-guide.md](../../docs/provider-guide.md). Under
  `CASCADE_NO_INPUT=1` this refuses immediately with a structured error
  naming `--key`/`--key-env` as the non-interactive alternative, rather
  than hanging on a browser that will never open.

Whichever form is used, the resulting material never lands anywhere but
the vault: intake builds a `secrets.Broker` over the platform `Custody`
backend (OS keychain on macOS, the D-Bus secret service on Linux, an
age-encrypted file vault elsewhere) and stores the credential there before
the registry record is written. The registry's `auth_ref` column holds
only the vault-key *name* — never the value; a value that is
credential-shaped is refused at the domain-validation layer before it can
reach storage.

`--no-verify` skips the live micro-verify call and records a warning
instead of a hard failure — useful for offline setup, never the default.

## 4. Key-pool lanes

A key-pool lane groups several credentials for the same provider behind
one logical lane name, so the registry — not the caller — decides which
member handles the next request. This is the registry-owned pool model:

- **`pool_index`** on each `LaneRecord` is the round-robin position within
  its pool, advanced by `Registry.AdvancePoolIndex`. Rotation picks the
  least-recently-used member whose `State` is currently available —
  `AdvancePoolIndex` never promotes a member the registry has marked
  unavailable.
- **429-demotion**: when a pool member's provider answers 429, its health
  state is updated atomically (`Registry.AtomicHealthUpdate`) and its
  `DemotionCount` increments. A demoted member is skipped by subsequent
  `AdvancePoolIndex` calls until it recovers.
- **Dead-key eviction**: a member that fails its health probe repeatedly
  is evicted from the pool's rotation, not deleted from the registry —
  eviction is a health-state transition an operator can inspect and
  reverse, never a silent removal.
- **Pool-stateless drivers**: a driver implementation never tracks which
  pool member it is — it receives one resolved credential per call and
  returns a typed exhaustion error (the frozen `KindQuotaExhausted` kind)
  when the credential it was given reports quota exhaustion. Pool
  selection and rotation live entirely in the registry, above the driver.

The conductor's own quota/spill policy (`internal/conductor`) is a
separate, higher layer: it orders which *lane* to try next across a
request's already capability- and sensitivity-filtered candidate set. It
never selects a pool member directly — that stays the registry's job via
`AdvancePoolIndex`.

## 5. Adding a driver

1. **Package layout.** Create `providers/<name>/` with a `New(...)
   (provider.ModelProvider, error)` constructor. The package imports
   `pkg/provider` for the interface and types, and nothing from
   `internal/` — drivers stay usable outside this module's own binary.
2. **Implement the five methods.** Translate `pkg/provider`'s
   vendor-neutral request/response types to and from the vendor's own
   wire shapes. Every error a driver returns must be a `*cascade.Error`
   with a taxonomy `Kind` — never a raw `fmt.Errorf`/`errors.New` escaping
   the driver boundary.
3. **Register with the shape-probe chain.** If the driver participates in
   `cascade provider add`'s automatic detection, add its probe to the
   anthropic-compat → openai-compat → gemini sequence in
   `internal/providers/intake` (Section 2 above) — do not invent a fourth,
   parallel detection mechanism.
4. **Wire health probes.** `internal/providers/health` owns the probe
   loop and eviction policy; a driver exposes whatever minimal endpoint
   the health package needs to distinguish "reachable" from "unreachable"
   from "cannot determine" — the last of those is always a distinct,
   visible outcome from "healthy", never folded into it.
5. **Wire usage accounting.** `internal/providers/usage` records
   per-request/per-token counts against the provider's aggregate row. A
   driver never writes usage rows itself; it returns `Usage` on every
   `ChatResponse`/`StreamEvent`, and the caller above it accounts.
6. **Test against recorded fixtures.** Every landed driver ships with
   fixture-backed tests, never a live network call in the default unit
   lane — a real-socket integration test, if one exists, sits behind the
   `integration` build tag.

## 6. Compliance statement

Cascade is capacity orchestration over legitimately authenticated
profiles the operator already owns — it is never a quota bypass, and no
driver or intake path in this codebase is built to evade a provider's own
rate limits or terms.

The tier-preservation pattern documents this as ordinary user
configuration, not a special case: an operator's `spill_order` can list
lanes tier-2 → tier-1 → tier-0, so a higher tier is consulted first and
lower tiers are preserved as fallback capacity rather than being drained
in parallel. Preservation is a `spill_order` the operator configures
(`internal/conductor`'s `[conductor.quota]` section) like any other lane
ordering — nothing in the routing layer treats a tier specially by name.
