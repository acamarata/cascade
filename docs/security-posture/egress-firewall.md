# Egress firewall

What stops content leaving this machine, where each stop is enforced, and
what each one cannot yet see. Written to be read by someone deciding
whether to trust a claim, so every gap below is named rather than
implied.

## One firewall per process

Every outbound path writes through one engine binding: the process
firewall. `cmd/cascade/egress_firewall.go` selects the vault custody once,
builds the non-elevated broker and `secrets.EgressVault` over it, and
installs the factory behind `egress.ProcessEngine`. Nothing else in the
tree builds an `egress.Engine` or supplies a value source.

**Exact-value coverage on every egress path.** The substitution pass has
two halves: the detector redacts a string with credential shape, and the
exact-value pass replaces a string that IS a stored secret even when it has
no shape at all. The second half only runs when the engine reads the real
vault. These paths now take their engine from the process firewall, so a
stored value of eight bytes or more leaves only as its vault reference:

| path | where |
|---|---|
| MCP responses | `internal/mcp` (`NewDefaultResponseMarshaler`) |
| bridge replies | `internal/bridge` (`NewFirewall`) |
| nself backend tool responses | `internal/plugins/nself_wiring.go` |
| remote plugin handshake | `internal/plugins/dispatch_remote.go` |
| wait-on-green / merge-on-green polling | `internal/ci` (engine injected by `cmd/cascade`) |
| provider intake and health probes | `cmd/cascade/provider_cmd.go`, `provider_health_cmd.go` |
| conductor execute and recall answers | `cmd/cascade` daemon wiring |

Three of these (ci polling, nself, remote dispatch) used to bind an empty
vault, so only the shape half ran on them. Each has a test that plants a
shapeless value in the vault and fails if it crosses verbatim.

**Custody unavailable is a refusal.** With no firewall installed,
`egress.ProcessEngine` returns `egress.ErrNoProcessFirewall`. A custody
backend that cannot be selected is returned with its own kind and cause,
and the failure is not cached: a later call succeeds once the vault is
available. The firewall is bound to the data directory it was built over;
a caller naming another directory is refused, so one directory's grants
are never read through another's vault. The daemon builds the firewall in its `egress-firewall` wiring
step, right after `status.get`, so a daemon that cannot open its vault
refuses to start and names that step.

**One detector, under the operator's settings.** Every scan site uses
`secrets.ProcessDetector`, one detector per process. Its configuration is
config.toml's `[secrets]` section (`entropy_floor`,
`confidence_threshold`); an absent section takes the shipped defaults. An
invalid value (out of range, or a string where a number belongs) or an
unknown key (a typo such as `confidence_treshold`) is refused with the key
named, and the daemon will not start on it. `keychain_backend` and
`clipboard_ttl` are accepted; other readers own them. The
section is read once at start; a hot reload of `[secrets]` is not applied
yet.

**Gates.** `internal/build/arch_detector_test.go` fails the build if
shipping code outside `internal/secrets` names
`secrets.DefaultDetectionConfig`, `secrets.NewDetector` or
`secrets.NewScrubber` (through any alias), if anything outside the process
firewall calls `egress.NewEngine`, or if any shipping type outside
`internal/secrets` implements the vault interface (an empty, map or other
stand-in, with its two methods in one file or two). The non-elevated
`NewEgressVault` read is limited to `internal/secrets` and `cmd/cascade`.
`internal/build/arch_secrets_test.go` fails it if `cmd/cascade` calls
`secrets.SelectCustody` anywhere but through its one host custody
selector.

**One host custody selection.** The process firewall, the `cascade vault`
commands, `cascade provider add`, `cascade doctor`'s vault checks and the
daemon's chat scrub vault all select custody through one selector with one
configuration (the cascade vault under the data directory), so they read
and write the same store.

**QA custody.** A `devkeys` development build run with
`CASCADE_TEST_FILE_VAULT=1` forces every one of those selections onto the
encrypted file vault in the same data directory, so a real-binary QA run
never touches the login keychain and the firewall still sees what `vault
set` stored. A release build ignores the variable.

**What this does not cover.** Custody selected inside other packages from
a configuration the CLI hands them does not go through that selector. The
bridge token vault follows the QA switch (its configuration takes the same
file-vault bit), but it is selected inside the plugin. The node keystore
and the CI token probe are outside both the selector and the QA switch.

## Thread privacy mode: enforcement

A conversation thread carries a privacy mode -- one of the four §5.16
sensitivity tiers. `cascade chat --local-only` and `--private` set it at
thread creation; absent any mode a thread is `restricted`, which is the
tier `provider.SensitivityTier`'s zero value already denotes, so the
default is fail-closed by construction.

Enforcement is at lane selection, in `internal/conductor/privacy.go`. It
is FILTER 0: it runs before capability, sensitivity, health, quota and
cost. A request the privacy table refuses never reaches dispatch -- the
quota stage, the first that would consult a lane for a call, is never
entered.

**Not yet reached in production.** No daemon component generates an
assistant reply today, so no conversation-class task reaches the router at
all, and nothing yet attaches a thread to the routing context. The gate is
built, exercised and exempted in `internal/build/testonly-allow.json`
against `P1-E45-W10-S89-T6`, the ticket that adds the reply pipeline and
must wire it. Until that lands, a thread's mode is *recorded* and not
*enforced*, because there is nothing to enforce it on. This page describes
what the gate does when it is reached, not a protection that is running.

**What ordering does and does not guarantee.** Because privacy runs first,
a refusal *caused by* the privacy table is reported as
`ErrSensitivityViolation` rather than as a capability miss. It does not
follow that every failure downstream of a privacy refusal names privacy:
if the table removes the capable lanes and leaves an incapable local one,
the caller sees `ErrNoCapableProvider` from FILTER 1, and only the explain
trail's `privacy:<mode>:N-filtered` flag says why the capable ones were
gone. Read the flags, not just the error.

### The table

Lane type is derived from the provider record's `BaseURL`, using the same
predicate the sensitivity pass (K/S-22.T3) uses, so the two gates cannot
disagree about a lane:

- **controller-local** -- a loopback host or a `unix://` socket. Loopback
  means a literal address in 127.0.0.0/8, `localhost`, or `::1`. It is
  checked as a literal because a prefix test admitted `127.evil.com`, a
  registrable domain that resolves wherever its owner points it; that was
  a real fail-open in the shared predicate, found by the independent
  review of this change and fixed with it.
- **external-api** -- a resolvable host elsewhere.
- **unresolved** -- no `BaseURL`, or one that does not parse, or one with
  no host. Nothing in this build can say which machine it reaches.

| mode | controller-local | external-api | unresolved |
|---|---|---|---|
| `local-only` | allow | **refuse** | **refuse** |
| `restricted` | allow | allow (see the gap below) | **refuse** |
| `internal` | allow | allow | **refuse** |
| `public` | allow | allow | allow |

A tier outside the four -- including one that was never set -- is read as
`restricted`, so the table is total over the whole value range and not
only over the four named tiers. `unresolved` is the ZERO VALUE of the
lane type for the same reason: a lane nobody classified must be the one
the table treats most strictly.

A refusal is `conductor.ErrSensitivityViolation` (`KindPolicyDenied`),
wrapped with a message naming the thread, the mode and a refused lane.
R-21.217 fixes that sentinel and `ErrNoLane` as the only two for this
condition; nothing here adds a third.

### Bridge lanes

R-14.60 strikes `pkg/bridge.Gate` and says bridge refusal is the
sensitivity pass's job. A bridge lane reaches another machine, so it is
never controller-local and the `local-only` row refuses it. There is no
direct bridge cell because there is no bridge vocabulary to key one on:
the provider registry's closed sets (`DriverKind`, `AuthType`,
`AccountKind`, `Tier`, `HealthStatus`, `CapacityBucket`) contain no
bridge member.

### What this does not yet do

Two gaps, stated plainly because a security page that implies more than
it enforces is worse than one that enforces less.

1. **`restricted` does not refuse external lanes.** The contract's
   restricted row reads "refuse ... any external-API lane not explicitly
   permitted for restricted content", and the registry carries no
   per-lane permit field to evaluate that against -- the same deviation
   the sensitivity pass records for its own restricted/internal/public
   legs. Refusing every external lane instead was rejected: `restricted`
   is the default for an unset mode, so that reading refuses every
   external lane for every request naming no tier. Closing this properly
   needs a registry trust-tier field, which R-14.60 declines to invent.

2. **`Policy.ExternalAllowed` is not enforced at lane selection.**
   `pkg/provider.Policy` says `false` "forces local-only placement
   regardless of Sensitivity", and node placement honours that. Lane
   selection does not read it at all. It is not wired in here because
   `false` is its zero value and no production caller sets it, so
   enforcing it would refuse every external lane for every request --
   a routing change that needs its own ticket, not a side effect of this
   one.

### Where it is proven

The independent review of this change also found, and this fixed: the
loopback prefix test above; a `privacy_mode` that was MISSPELLED on the
wire being coerced to `restricted` rather than refused, which widens a
caller who typed `local_only` onto external lanes; and a foreign key on
the marker table that would have made the write ordering fail outright the
moment `PRAGMA foreign_keys` was turned on.


`internal/conductor/privacy_test.go` asserts every cell of the table by
naming each expected answer rather than re-deriving it from the code
under test, and `TestPrivacyModeIntegration` asserts the refusal, its
message and that dispatch was never reached. Three mutations -- letting
`local-only` through to external lanes, classifying unresolved lanes as
external, and dropping the thread id from the refusal -- each turn the
suite red.

## Conversation scrub pipeline

A different boundary from the two above: this one runs on `chat.append_turn`,
before a turn's segments are ever committed to `Store` or mirrored over SSE,
not at lane selection or on the outbound-substitution path. It is
`internal/conversation/scrub.go`'s four phases, over H/S-15's real
components -- `secrets.Detector.ScanCertain` (detect),
`secrets.QuarantineStore.Put` (quarantine), `secrets.Broker.SetRename`
(vault), `secrets.Rewriter.Rewrite` followed by a residual re-scan (rewrite)
-- and it is a DIFFERENT thing from the `Substitutor` seam sse.go's own
ordering comment names: substitution rewrites the SSE echo of already-stored
content; the scrub pipeline decides what gets stored in the first place.

**What it does.** `Adapter.handleAppendTurn` calls `scrubSegments`
immediately after decoding a turn's segments, before either the journal
write or the plain `Store.AppendTurn`/`AppendSegment` path, and before
`emitTurnAppended`'s SSE mirror. Detection is TURN-SCOPED: the segments are
joined in order and scanned once, so a credential split across two segments
cannot hide in the seam -- and a span that straddles a boundary refuses the
turn rather than being rewritten across two stored rows. The remaining
phases are ordered across the whole turn: every hit is quarantined
(`QuarantineStore.Put`, one entry per hit) before the first vault write,
every hit's bytes are stored (`Broker.Set` with `SetRename`, so an
operator's existing entry is never overwritten and two same-named hits get
two entries) before the first rewrite, and each segment is then rewritten so
every hit becomes its typed tag (`<apikey>NAME</apikey>`, secrets/tags.go's
grammar) where NAME is the name the vault ACTUALLY used -- so the tag is a
working reference to the entry holding that span. The output is re-scanned,
per segment and once over the rejoined turn, for anything the detector would
still flag. Only then do the quarantine entries get released
(`ReleasePromoted`).

**Fail closed, all the way.** Any failure -- a straddling span, the
quarantine ledger's own write, the vault's `Set`, the rewrite, or a residual
match after it -- refuses the whole `chat.append_turn` call. Nothing partial
is ever committed: the turn is refused before `Store.AppendTurn` is even
called, so there is no rollback to reason about. A turn refused AFTER the
vault phase leaves an inert vault entry: nothing references it (no tag
reached storage and no turn was forwarded), and the append-only quarantine
ledger still carries the live entry recording the detection, which is the
record of what happened. Each refusal also publishes a
`security.scrub_divergence` event on the same `EventBus` the SSE mirror
uses, on its own `security` namespace, naming which phase failed and a
static reason string -- never turn content.

**The blast radius of a certain hit, stated plainly (CR-C).** A value the
scrub writes into the vault does not stay inside this one turn. It becomes a
vault entry, and the egress substitution pass described at the top of this
page replaces vault values SYSTEM-WIDE in outbound content. So a
false-positive certain hit -- an ordinary string the detector was confident
about -- will thereafter be substituted out of outbound payloads wherever it
appears, not only in the conversation that produced it. The only limiter
today is `ScanCertain`'s confidence threshold: there is no allow-list, no
operator confirmation step in this path, and no scoping of a vaulted value
to the thread it came from. That is a deliberate fail-closed trade (a missed
secret is worse than an over-substituted string), and it is the property to
revisit if the detector's precision ever drops.

**What this does not yet do.** `NewDefaultScrubPipelineOverVault` is wired
into `cmd/cascade/chat_wiring.go` (the daemon's real chat.* composition root)
over a custody the composition root SELECTS AND INJECTS, under the same
`vaultService` label `cascade vault` reads, so a real deployed daemon does
scrub. (The custody is a parameter because selecting one inside that wiring
reached the operator's real OS keychain from every test that ran it, where
the repository's keychain gate could not see it.) But
Phase 1's contract line ("on detector error: quarantine + divergence")
names a failure mode the real `secrets.Detector.Scan`/`ScanCertain`
cannot produce: both are pure, total functions with no error return.
What Phase 1 actually fails on, and what its error handling is written
against, is its own quarantine-ledger write. No production segmenter
(U/S-45.T2) exists anywhere in this tree yet to receive a post-scrub
turn; the ordering this page describes against "a future segmenter" is
proven with a test-only stand-in fed from the same post-scrub bytes
`Store` received, not a real call site.
