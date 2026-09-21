# Egress firewall

What stops content leaving this machine, where each stop is enforced, and
what each one cannot yet see. Written to be read by someone deciding
whether to trust a claim, so every gap below is named rather than
implied.

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
