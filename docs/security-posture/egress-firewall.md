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
