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
