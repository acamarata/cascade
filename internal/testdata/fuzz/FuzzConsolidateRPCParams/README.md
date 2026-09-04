# FuzzConsolidateRPCParams seed corpus

Seeds for the `memory.consolidate` JSON-RPC params decoder
(`internal/memory/rpc.go`'s `decodeParams` decoding a
`memory.ConsolidateParams`, driven by `FuzzConsolidateRPCParams` in
`internal/memory/rpc_admin_test.go`).

Every byte the decoder sees arrives from a peer over the daemon socket,
so the corpus is shaped around what a peer can send, not around what the
CLI happens to send. This particular verb retires a user's own records,
so its rehearsal flag is the one field a hostile or buggy peer has any
leverage over.

| Seed | Provenance | What it covers |
|---|---|---|
| `seed001` | Hand-authored, 2026-09-04. The exact params `cascade memory consolidate --dry-run` produces. | The well-formed rehearsal shape. |
| `seed002` | Hand-authored, 2026-09-04. A real run carrying a field this build does not know. | Forward compatibility: an unknown member must not turn a real run into a refusal, nor a rehearsal into a real run. |
| `seed003` | Hand-authored, 2026-09-04. A truncated object. | The unterminated-JSON refusal path. |
| `seed004` | Hand-authored, 2026-09-04. A string where a boolean belongs, carrying the invalid UTF-8 sequence `C3 28`. | Two refusal paths at once: a type mismatch on the one field that decides whether records are destroyed, and a non-UTF-8 string value. |

The fuzz target asserts the absolute contract: no input panics, and every
refusal is a `pkg/cascade` taxonomy error rather than a bare one. A decode
failure is a correct outcome, not a finding.
