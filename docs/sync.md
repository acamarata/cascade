# Sync engine

The sync engine (`internal/sync`) is the server-local-node synchronization
core: domain classes, per-domain cursors, a chunked ssh transfer, and the
sync egress class. Merge strategies and the CLI/RPC surface land in later
tickets; this document covers the engine core only.

## Domain classes

Every syncable record family maps onto one of the closed twelve cascade.db
storage domains (`internal/storage`). The registry (`internal/sync/domains.go`)
never creates a new domain — it is a mapping over the existing set:

| Family | Storage domain | Sync class |
|---|---|---|
| config | `config` | synced |
| registry | `config` | synced |
| accounts | `config` | server-primary (metadata only) |
| phase-state | `config` | synced |
| memory | `memory` | synced |
| conversation | `context` | synced |
| blobs | `blobs` | synced |

The sync-class enum is `local-only | synced | server-primary`, plus
`synced-append` for plugin-scoped domains only. A domain or plugin storage
key that is never explicitly registered is **local-only and never syncs** —
there is no default merge. The vault (`secrets` domain) is not, and cannot
be, a registered domain: the exclusion is structural, not a runtime check.

Plugin-scoped domains declare their class through the plugin manifest v2
`storage.sync` key (default `local-only`).

## Sensitivity filter (pre-serialization)

Every record also carries its own immutable sensitivity tier. Before a
record is ever serialized for transfer, `internal/sync/filter.go` checks:

1. the record's domain+subkind is registered and not local-only;
2. the record's own tier is not `local-only` or `restricted`.

A record failing either check is never serialized. It is not silently
dropped: its exclusion is journaled (record id, reason, policy version)
and the domain's cursor advances past it — the cursor only ever advances
over records that were durably applied or explicitly excluded. An
excluded record is re-scanned whenever the policy or classification
version advances past the one it was excluded under.

## Cursors

Per-domain-subkind cursors persist through the storage `Store`
abstraction, monotonic and resumable across process restarts. A cursor
regress (a new position at or below the current one) is refused as a
typed conflict, never silently accepted as an implicit resync.

## Chunked transfer

The wire format (`internal/sync/chunk.go`) frames a stream into fixed
chunks: magic, version, stream id, sequence, total, payload length, and a
transport-layer BLAKE3 hash of the payload. `FuzzSyncChunkDecode` proves
the decoder never panics on malformed input and fails closed on every
truncated, reordered, or corrupted shape.

The transfer loop (`internal/sync/transfer.go`) rides whatever
`io.ReadWriteCloser` the caller supplies — production callers hand it the
S-36.T3 ssh tunnel's forwarded channel; no new dialer or ssh stack is
introduced here. Receipt is strictly sequential: a chunk out of order,
from the wrong stream, or truncated is refused immediately, and the
receiver reports exactly how many chunks it durably processed so a
dropped connection resumes from that point rather than restarting or
silently completing with missing data.

Blob content (`internal/sync/staging.go`) lands in a staging file first.
A blob is admitted to its final content-addressed path only when the
recomputed BLAKE3 digest of the staged bytes equals the *declared*
content address — a value established independently of the transfer, not
one the transfer itself supplied. A mismatch discards the staged bytes;
the final path is created only by one atomic rename, so an interrupted or
corrupt transfer never becomes visible as a complete file. Resuming an
interrupted staged transfer requires a durable, consistent sidecar
cursor; its absence or inconsistency is an explicit refusal to resume,
never a silent restart.

## Sync egress class

The sync engine registers `EgressClassSync` (`internal/hooks/egress`,
§D-30) at its own package init. Every outbound sync payload transits the
substitution and sensitivity pass on this class before it reaches the
wire, in addition to (never instead of) the pre-serialization filter
above.

## Merge strategies (S-38.T2)

Each domain merges by the rule its content needs, and a domain nobody
mapped never syncs at all.

| Domain | Strategy | What it means |
|---|---|---|
| `config` | server-primary LWW | the server wins; the losing local write is journaled |
| `registry`, `accounts` | metadata-only server-primary | as above, and a record whose own tier forbids replication is refused on **both** sides |
| `memory`, `conversation` | append-merge + tombstones | union of record ids; a delete dominates |
| `phase-state` | git-carried | fetch and fast-forward only; never engine-merged |
| `blobs` | content-address union | presence or absence; there is nothing to lose |
| anything unmapped | none | fail-closed, and not an error — see below |

**An unmapped domain is not an error.** It resolves to no-sync. An error
would make a new domain break sync until somebody mapped it, and the
pressure would then be to add a permissive default; a silent no-sync makes
a new domain safe by default and visible the moment somebody expects it to
replicate.

### Ordering: never the clock

Two copies of one record are compared by the server-assigned monotonic
revision, then the persisted hybrid logical clock, then the writer's node
id. **Wall time is carried for provenance and never compared.** A laptop
whose clock is a day fast would otherwise win every conflict against the
server for a day — silently, and in the direction that loses the
authoritative copy.

The triple is a total order, which is what makes taking the maximum per
record id commutative, associative and idempotent — and therefore what
makes two peers merging in different orders reach the same state.

### Tombstones

A tombstone dominates a concurrent update in the record domains, in either
merge order. A delete that lost would resurrect a record somebody removed
on purpose, which is the worst outcome this merge can produce.

`[sync].tombstone_retention` is `never` in P1. Pruning a tombstone
resurrects the record it deleted for every peer that was offline across the
prune. A peer whose cursor predates the oldest retained tombstone is
refused with `ErrCursorTooOld` and must full-resync — it cannot be caught
up incrementally without handing it back every record it should have
removed.

Config is different: there a delete is an ordinary value competing in the
total order, so a key deleted and then re-set at a higher revision is set.

### Phase state is never engine-merged

Carriage is fetch plus `--ff-only`. A divergence is refused with
`ErrPhaseStateDiverged`, journaled with both refs, and surfaced through
`sync conflicts list` for a person to resolve in the repository. A
three-way merge of two ticket trees can produce a tree that is valid YAML
and describes a phase nobody planned.

### The conflict journal

Every merge that had to choose writes down the domain, the strategy, both
sides (node, revision, HLC, content hash) and the resolution. A
server-primary merge discarding a local edit is correct and is still
somebody's work disappearing, so the discard is recorded with both content
hashes: an operator who wonders where their change went gets an answer.

Records that merged without a choice — identical copies, or one causally
superseding the other — are **not** journaled. A journal full of entries
where nothing was lost is one nobody reads.

## Who may sync what: the domain x tier table

Sync eligibility is a closed table of (tier, domain) pairs, committed as
`internal/sync/testdata/domain_tier.golden`. Anything not in it is no-sync.

| Tier | Domains |
|---|---|
| `controller` | all of them |
| `worker-trusted` | `config`, `phase-state`, `blobs`, `registry` |
| `paired-device` | none, in P1 |

A worker-trusted node runs dispatched work, so it needs configuration, the
phase state that says what the work is, the blobs the work reads and the
registry metadata that names providers. It does **not** get memory,
conversation or accounts: none is needed to run work, and each would be a
standing copy of something personal on a machine whose whole purpose is to
be disposable.

A paired device syncs nothing in P1 — not because it could not, but because
nothing in P1 decides what a phone should hold, and the answer to an
undecided question about personal data is not "some".

A predicate ("tier rank at least N") would be shorter and would answer for
pairs nobody has thought about. The table is a golden so that changing it
is a visible diff in review.

### Three gates, all of which must pass

Before any record is serialized for a peer: the domain must be registered
and synced; the peer's tier must be permitted that domain; and the
record's **own** sensitivity tier must allow it to leave. Nothing in this
path ever widens a tier.

## Fail-closed summary

| Condition | Behavior |
|---|---|
| Unregistered domain/subkind | Refused, treated as local-only |
| Record tier local-only or restricted | Refused, journaled, cursor still advances |
| Cursor regress | Refused as a typed conflict |
| Truncated/reordered/corrupt chunk | Refused, transfer stops at the last verified chunk |
| Blob digest mismatch | Staged bytes discarded, final path never created |
| Missing/inconsistent resume state | Refused to resume, never guessed |
| Unresolvable sensitivity tier | Resolves to `restricted` (fail closed) |
| Unmapped domain at merge time | No strategy, never a default merge |
| (domain, tier) pair not in the table | No sync |
| Peer cursor older than the oldest tombstone | Refused; full resync required |
| Phase state diverged | Refused and journaled; never engine-merged |
| Blob never admitted by staging | Refused; the union carries admitted blobs only |

## Acceptance drill (S-38.T5)

The two-machine runbook. Everything below runs as one script; the only
difference between the rehearsals and the acceptance is which machine is on
the other end.

### Target resolution

The drill reads its peer from the environment, never from a prompt — so a
non-interactive run can supply one, and `CASCADE_NO_INPUT=1` changes
nothing here:

| Variable | Meaning |
|---|---|
| `CASCADE_ACCEPTANCE_SYNC_TARGET` | the enrolled second machine, `user@host[:port]` (port defaults to 22) |
| `CASCADE_ACCEPTANCE_SYNC_NODE_ID` | that machine's enrolled node id |

**Every failure is a typed error, and none of them is a loopback
substitution.** Nothing configured is `unavailable`; something configured
wrongly is `invalid-input`; a target set without a node id is refused
because the drill asserts trust-tier eligibility for a *specific* enrolled
node and cannot infer which one. A harness that quietly fell back to two
engines in one process would report the acceptance as passed on evidence
nobody asked for, which is the one outcome this design exists to prevent.

### The three lanes

| Lane | Runs | Proves |
|---|---|---|
| In-process rehearsal | always | the script |
| Real-sshd rehearsal (`integration`) | CI, allowed-fail | the script over the real transport |
| The drill | with a target configured | the acceptance |

The two rehearsals call the *same* function the drill does. What is
threaded through them is the transport: the in-process lane hands the
encoded batch straight across, the sshd lane ships it to a remote file over
a real ssh session and reads it back through a real remote `cat`.

### What is injected

The adversarial corpus under `internal/sync/testdata/conflicts/`, not
fixtures written for the drill. Those cases were built to break the merges
and are digest-gated, so a drill that invented its own conflicts would be
proving the merges against inputs chosen after the merges existed.

One case per record domain class: config (delete-vs-write), accounts
(revision tie-break), registry (24h clock skew), memory (concurrent
update), conversation (double tombstone). Blobs and phase-state are
asserted separately — the blob union is keyed by content address and cannot
collide on a record id, and phase state is git's answer rather than a
record merge.

Fixture records are given an explicit sensitivity tier before they are
shipped, because an **unset tier resolves to restricted**: left unset,
every one would be filtered out of every batch and the drill would assert
convergence over an empty wire.

### What is asserted

- **Both directions, then convergence** — by record id *and content hash*.
  Two sides holding the same ids with different contents is the divergence
  that looks like convergence from a length check.
- **No silent loss**, stated over the ids each side held *before* the
  drill. Asserting it afterwards asserts only that the merge agrees with
  itself.
- **The planned strategy ran** — the S-38.T2 table verbatim, checked on
  what the round trip selected. A merge chosen wrongly resolves perfectly
  and still produces the wrong answer.
- **The losing config side is journaled**, with both identities.
- **A tombstone survives both directions.** One that survived only one
  would resurrect on the next sync.
- **Cursors never regress**, and a regress is refused rather than read as a
  resync-from-scratch signal.
- **An interrupted transfer resumes from the last acknowledged chunk** —
  the drop costs the chunks not yet sent, never the ones already applied.

### Security invariants, on the wire

Asserted on what the receiver *decoded*, not on raw wire bytes: a payload
is base64 inside the batch's JSON, so a `contains` check over the wire
would fail to find a payload that was plainly there and "prove" an
exclusion that never happened.

- No vault-domain record is admitted at any declared tier — the refusal is
  structural, because a vault record declared public is still a vault
  record.
- Local-only material does not leave the owning device, and its exclusion
  is journaled with a reason.
- Trust-tier eligibility decides what crosses: a worker-trusted peer is
  refused the memory domain, and a controller is not.
- The accounts domain carries metadata only; a restricted record is dropped
  from **both** sides and journaled, because one arriving is as much a
  fault as one leaving.

### The elevated resolve

Discarding the server's copy routes the 06 §5.14 flow through the same
`sync.Deps` the CLI and the RPC use. Refused when the gate says no, refused
when there is no gate at all, and honored with the result recording that
the gate ran. Keeping the server side is not elevated: it re-affirms what
the merge already decided.

### Evidence a completed drill must show

A validated record — target and node id, every domain injected, the merge
that decided each, the conflict-journal dump with both sides, convergence,
and the red-team result. It refuses to validate when a domain has no
recorded strategy, when the sides did not converge, or when the security
result is not clean, so an acceptance record cannot become a formality.

The real drill against the enrolled second machine is one of the 06 §7
owner prerequisites. It gates that evidence only — never the harness, never
either rehearsal.

## CLI (S-38.T3)

The `sync` noun mirrors the `sync.*` RPC methods one for one:

| Verb | RPC method | Reads | Writes |
|---|---|---|---|
| `sync status` | `sync.status` | domain classes, strategies, tier eligibility, cursors, open-conflict count | — |
| `sync run [--domain <d>]` | `sync.run` | the eligible domain set | one engine run per domain |
| `sync conflicts list` | `sync.conflicts_list` | the journal above | — |
| `sync conflicts resolve <id> --keep server\|local` | `sync.conflicts_resolve` | the journal | the resolution |

Three properties of this surface are load-bearing rather than cosmetic:

**Status lists every registered domain, including the ones this peer's
tier may not sync.** The report exists to explain why something is not
syncing, and omitting the domain is the least useful possible answer to
that question.

**A cursor position that could not be read is reported as unread, never as
zero.** Zero is a real position — a domain that has never synced is at
zero — so `position` and `position_known` are separate fields, and the
human rendering prints `?`.

**Discarding the server-primary side is the only elevated resolution.**
`--keep server` re-affirms what the merge already decided and changes
nothing; `--keep local` throws the server's copy away and overrides the
authority a server-primary domain is defined by (06-FORGE-SPEC §5.14). The
side is chosen by flag, never by a blocking prompt, so a non-interactive
run can make the choice — but the discarding side is additionally refused
on Windows (tier-2), refused under `CASCADE_NO_INPUT=1` without `--yes`,
and refused outright when no elevation gate is wired. It is never exposed
as an MCP tool.

Full verb reference, including exit codes and the current implementation
status of `sync run`: [docs/cli-reference/sync.md](cli-reference/sync.md).
