# internal/sync/testdata — provenance (Art.2.2)

## fuzz/FuzzSyncChunkDecode/

Seed corpus for `internal/sync.FuzzSyncChunkDecode` (`internal/sync/chunk_test.go`),
in Go's native `go test fuzz v1` corpus encoding (required because this
directory sits at the package's own auto-scanned
`testdata/fuzz/<FuzzName>/` path). Every seed is hand-authored — a chunk
frame is this ticket's own wire format with no external corpus to
harvest from:

- `seed_valid_frame` — one well-formed `Chunk` frame (StreamID 7, Seq 0,
  Total 1), built with the same encoder under test, so mutation starts
  from a structurally valid frame as well as adversarial ones.
- `seed_truncated_header` — the same frame cut short mid-header, the
  truncated shape `Decode` must refuse with `KindIntegrity`.
- `seed_garbage` — arbitrary bytes carrying no valid magic at all.

## Real-counterpart lane (Art.2)

The chunked transfer's real-counterpart proof
(`TestSyncChunkedTransferRealSSHD`, `internal/sync/transfer_integration_test.go`,
`integration` build tag) drives the wire framing and transfer loop over a
REAL `/usr/sbin/sshd` spawned on loopback for the test's own duration,
using `internal/nodes`' existing `ExecDialer`/`ExecSession`
(P1-E17-W4-S36-T5's production ssh transport — this ticket introduces no
new dialer or ssh handshake code). It generates a fresh ephemeral ed25519
keypair per run (never a committed key) and produces no recorded fixture:
every byte exchanged is generated and verified within the test itself, so
there is nothing here to provenance-stamp beyond this note.

## conflicts/ — the merge corpus (Art.2.2)

Sixteen adversarial conflict fixtures, **copied verbatim** from
`internal/syncmerge/testdata/fixtures/` as P1-E17-W4-S38-T2's acceptance
corpus.

| | |
|---|---|
| Origin | the P1-E13-W3-S27-T5 risk spike |
| Tool | hand-authored (no generator) |
| Envelope | the B/S-03.T3 per-domain JSON export shape |
| Copied | 2026-09-17, byte-for-byte |
| Digest | `291a77c5f48eba2b3d40f71b7255a227478baae0fc3224a00a3b71bf884b1cf7` |

**The digest is a gate, not a note.** `TestSpikeFixtureDigest` recomputes
it (BLAKE3 over per-file `BLAKE3(filename + "\n" + contents)`, in sorted
filename order) and fails when the corpus is absent or altered. The value
is recorded in `docs/adrs/ADR-sync-merge-semantics.md`, and a fixture may
only change alongside a revision of that ADR.

The reason is that these files are the evidence that the merges converge
under inputs somebody deliberately constructed to break them. Evidence that
can be edited to match the implementation is not evidence: the way this
suite would fail silently is a fixture being "fixed" to agree with a merge
that had regressed.

**Copied rather than read across packages** so this package's tests do not
depend on a spike package whose whole purpose was to be deleted — and the
digest is what makes the copy safe: a divergence between the two copies
fails here.

**Domain labels are the spike's**, not the sync registry's subkinds:
`blob` here is the registry's `blobs`, and `context` covers both memory and
conversation records. Renaming them would change the digest, and the digest
is what makes the corpus evidence.

Config, context and blob fixtures carry no `expected` field on purpose
(R-21.219): the record domains assert PROPERTIES — commutativity,
idempotence, associativity, tombstone dominance, no silent loss — which
hold or fail against any input. Only the three `phase-state` cases record
an outcome, because there the answer is real git's to give.
