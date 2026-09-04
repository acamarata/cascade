# FuzzReviewRPCParams seed corpus

Seeds for the `memory.review.*` JSON-RPC params decoder
(`internal/memory/review/rpc.go`'s `decodeParams` decoding a `ListParams`
and an `ActParams`, driven by `FuzzReviewRPCParams` in
`internal/memory/review/queue_test.go`).

Every byte the decoder sees arrives from a peer over the daemon socket, so
the corpus is shaped around what a peer can send, not around what the CLI
happens to send. The fields a hostile or buggy peer has leverage over are
the action name — which decides whether a record is made durable or a
promotion taken back — and the defer window, which decides how long a
candidate is hidden from the only surface that reviews it.

| Seed | Provenance | What it covers |
|---|---|---|
| `seed001` | Hand-authored, 2026-09-04. The exact params `cascade memory review project/a-note --auto-approve` produces. | The well-formed action shape. |
| `seed002` | Hand-authored, 2026-09-04. A defer carrying a field this build does not know. | Forward compatibility: an unknown member must not turn a defer into a refusal, nor into a different action. |
| `seed003` | Hand-authored, 2026-09-04. A truncated object. | The unterminated-JSON refusal path. |
| `seed004` | Hand-authored, 2026-09-04. A number where the action name belongs and a string where the defer window belongs, with the invalid UTF-8 sequence `C3 28` inside the address. | Three refusal paths at once: a type mismatch on the field that decides whether a record is written, a type mismatch on the window, and a non-UTF-8 address. |
| `seed005` | Hand-authored, 2026-09-04. A section this build does not know, an empty address, and a negative defer window. | The three post-decode validations: unknown section, unusable address, out-of-range window. |

The fuzz target asserts the absolute contract: no input panics, and every
refusal is a `pkg/cascade` taxonomy error rather than a bare one. A decode
failure is a correct outcome, not a finding.
