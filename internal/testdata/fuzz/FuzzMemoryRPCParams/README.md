# FuzzMemoryRPCParams seed corpus

Seeds for the `memory.*` JSON-RPC params decoder
(`internal/memory/rpc.go`'s `decodeParams`, driven by
`FuzzMemoryRPCParams` in `internal/memory/rpc_test.go`).

Every byte the decoder sees arrives from a peer over the daemon socket,
so the corpus is shaped around what a peer can send, not around what the
CLI happens to send.

| Seed | Provenance | What it covers |
|---|---|---|
| `seed001` | Hand-authored, 2026-09-04. The exact params `cascade memory remember "a remembered note" --type project --name a-note --provenance session-1` produces. | The well-formed `memory.remember` shape: every field populated. |
| `seed002` | Hand-authored, 2026-09-04. The params `cascade memory recall widgets --k 10 --type user` produces. | The well-formed `memory.recall` shape, including the numeric `k` field. |
| `seed003` | Hand-authored, 2026-09-04. The params `cascade memory list --type reference --limit 2 --cursor reference/aaa` produces. | The well-formed `memory.list` shape, including a cursor that is a canonical address. |
| `seed004` | Hand-authored, 2026-09-04. A truncated object whose `id` carries the invalid UTF-8 sequence `C3 28`. | Two refusal paths at once: unterminated JSON, and a non-UTF-8 string value inside an address field. |

The fuzz target asserts the narrow, absolute contract: no input panics,
and a decoded `id` that is not a `<kind>/<name>` pair is never accepted by
`ParseAddress`. A decode failure is a correct outcome, not a finding.
