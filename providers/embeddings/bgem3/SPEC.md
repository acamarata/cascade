# BGE-M3 sidecar protocol, version 1.0

This is the wire contract between cascade and a local BGE-M3 embedding
sidecar: a separate process that holds the model and answers batch embed
requests. It is a **first-party** protocol — specified here, implemented on
the client side by this package, and implemented on the server side by
cascade's own sidecar artifact. There is no external standard to conform
to, so this document is the whole contract: an implementer who has only
this file must be able to write a conforming sidecar.

Everything below is binding. Where the document says MUST, a client that
observes a violation refuses the response rather than working around it.

## Status in P1

**The sidecar does not exist yet.** It ships as a separate post-P1 artifact
(01-FEATURE-INVENTORY, Local-LLM row: "native sidecar ships as separate
post-P1 artifact; seam built + integration-tested-when-present"). What
ships in P1 is this specification and the client that speaks it.

Concretely, in P1:

- The client is **not registered** as an active embedder lane. Nothing in
  the embed pipeline reaches it, no configuration key selects it, and no
  command exercises it. Local embedding is an absent capability, documented
  as absent (Art.1.3), not a capability that appears to work.
- A call to the client with no sidecar listening returns a
  `KindUnavailable` taxonomy error. It never returns a zero vector, a
  truncated vector, or a padded one. A fabricated embedding is
  indistinguishable from a real one once it is in an index, and produces
  confident nonsense at query time that no downstream check can catch; the
  only recovery is a full reindex. So the client refuses.
- The client's conformance to this spec is asserted in P1 against a
  spec-conformance server that exists only in `conformance_test.go` and is
  never shipped. That is a test of the **client**, and this document says so
  plainly rather than letting it be mistaken for evidence that a real
  sidecar works.
- The client ↔ real-sidecar integration test runs when the post-P1 sidecar
  artifact exists. It is deferred with the artifact, by the same ruling.

## Transport

A stream-oriented, ordered, reliable, bidirectional byte stream. The
reference deployment is a unix domain socket. The client is written against
a stream, not a socket — it takes an injected opener that yields any
`io.ReadWriteCloser` — so a sidecar may equally be reached over a loopback
connection or over a pipe pair to a child process. Nothing in this protocol
depends on which.

**One call, one connection.** For each embed request the client opens a
connection, writes exactly one request frame, reads exactly one response
frame, and closes. There is no pooling, no pipelining, no multiplexing and
therefore no request identifiers: a response is correlated with its request
by being the only thing on that connection.

This is deliberate. A pooled connection that is abandoned mid-response —
by a timeout or a cancellation — holds the unread tail of that response,
and the next caller to take it from the pool reads that tail as if it were
its own answer. Connection-per-call makes that class of desynchronization
unrepresentable: an abandoned connection is closed, and a closed connection
cannot mislead anyone.

The sidecar MUST close the connection after writing its response frame, and
MUST tolerate a client that closes at any point, including mid-request.

## Framing

Each message on the wire is one frame:

```
+--------+--------+--------+--------+============================+
|            length (uint32)        |   payload (JSON, UTF-8)    |
+--------+--------+--------+--------+============================+
```

- `length` is 4 bytes, **big-endian**, unsigned, and counts the payload
  bytes that follow it. It does not include itself.
- `payload` is exactly `length` bytes of UTF-8 JSON: one object.
- `length` MUST be greater than 0. A zero-length frame is a protocol
  violation, not an empty message.
- `length` MUST NOT exceed **16777216** (16 MiB). A receiver that is told
  more MUST refuse the frame before reading it and MUST NOT allocate the
  declared size. The cap exists so a peer cannot exhaust the other side's
  memory by announcing a payload it never sends; 16 MiB is roughly four
  thousand 1024-dimension float32 vectors as JSON, an order of magnitude
  above any batch this pipeline groups.
- There is no trailing delimiter and no padding. The byte after a frame's
  payload is the first byte of the next frame, if any.

A receiver MUST treat all of the following as framing failures and refuse
the message: fewer than 4 header bytes; fewer than `length` payload bytes
before end of stream; `length` of 0; `length` over the cap; a payload that
is not a JSON object.

## Version negotiation

The version stamp is `MAJOR.MINOR`, both non-negative decimal integers.
The current version is **`1.0`**.

- Every request carries `protocol_version`. Every response carries
  `protocol_version`.
- A **MAJOR** difference is incompatible. A client that receives a response
  whose major version differs from its own refuses it (`KindUnsupported`)
  and does not attempt to interpret the payload.
- A **MINOR** difference is compatible. Minor revisions may only add
  optional members; they may never change the meaning of an existing member
  or remove one. A receiver ignores members it does not recognize.
- An absent, empty, or unparsable `protocol_version` is refused, not
  assumed to mean the current version. A peer that does not state its
  version has not agreed to a contract.

Negotiation is per-call and stateless: there is no handshake message. The
request's stamp tells the sidecar what the client speaks, and the
response's stamp tells the client what it got. A sidecar that cannot speak
the client's major version SHOULD answer with an `unsupported` error at its
own version rather than closing silently, so the client can report which
versions disagreed.

## Request

```json
{
  "protocol_version": "1.0",
  "op": "embed",
  "model": "bge-m3",
  "dimensions": 1024,
  "inputs": ["first text", "second text"]
}
```

| Member | Type | Meaning |
| --- | --- | --- |
| `protocol_version` | string | The client's version stamp. Required. |
| `op` | string | The operation. Version 1 defines exactly one: `"embed"`. |
| `model` | string | The model identity the client is configured for. |
| `dimensions` | integer | The vector width the client expects, > 0. |
| `inputs` | array of string | The batch. Never empty (see below). |

Notes binding on both sides:

- **`inputs` is never empty.** An empty batch is answered by the client
  locally and no connection is opened, because there is nothing to ask. A
  sidecar that nonetheless receives an empty array MAY answer with an empty
  `vectors` array.
- **Text is embedded as given.** Truncation, normalization, and prefixing
  change the resulting vector, so they are the pipeline's decisions, made
  once, upstream. The sidecar MUST NOT apply its own.
- `model` and `dimensions` are sent so the sidecar can refuse a mismatch
  itself, at the point where it knows what it loaded. Both sides check;
  neither side relies on the other having checked.

## Response

Success:

```json
{
  "protocol_version": "1.0",
  "model": "bge-m3",
  "dimensions": 1024,
  "vectors": [[0.1, -0.2, "..."], [0.3, 0.4, "..."]]
}
```

Failure:

```json
{
  "protocol_version": "1.0",
  "error": { "code": "invalid_input", "message": "input 3 exceeds the model's context window" }
}
```

| Member | Type | Meaning |
| --- | --- | --- |
| `protocol_version` | string | The sidecar's version stamp. Required on both shapes. |
| `model` | string | The model that actually produced the vectors. |
| `dimensions` | integer | The width of every returned vector. |
| `vectors` | array of arrays of number | One vector per input, in input order. |
| `error` | object | `code` and `message`. Present only on failure. |

Binding rules:

- **`vectors` and `error` are mutually exclusive.** A response carrying
  both is a contract violation; the client refuses it rather than choosing
  which to believe.
- **Positional correspondence.** `vectors[i]` is the embedding of
  `inputs[i]`. Reordering, deduplicating, dropping or padding the batch is
  a violation even when the same set of vectors comes back. A client cannot
  detect reordering structurally, which is exactly why it is stated as a
  binding rule on the sidecar.
- **All or nothing.** A sidecar that fails on one input fails the whole
  call. There is no partial result and no per-item error member.
- **Every vector is exactly `dimensions` long**, and every component is a
  finite number. JSON has no literal for NaN or infinity; a sidecar MUST
  NOT invent one (`NaN`, `Infinity` as bare tokens are not JSON and are
  refused as a malformed payload).
- **`model` and `dimensions` describe what was actually produced**, not
  what was asked for. A sidecar that loaded a different model reports the
  one it loaded and lets the client refuse. Reporting the requested
  identity to make the call succeed would silently mix two embedding
  spaces in one index, which nothing downstream can detect.

## Error model

`error.code` is drawn from this closed set. Each maps to one kind in
cascade's error taxonomy (`pkg/cascade`):

| `code` | Taxonomy kind | Meaning |
| --- | --- | --- |
| `invalid_input` | `KindInvalidInput` | The batch is malformed or an input is unusable (too long, wrong encoding). |
| `unsupported` | `KindUnsupported` | The operation or protocol version is recognized but not supported. |
| `unavailable` | `KindUnavailable` | The sidecar is up but cannot serve right now (model still loading, backend down). |
| `timeout` | `KindTimeout` | The sidecar's own work exceeded its deadline. |
| `canceled` | `KindCanceled` | The sidecar abandoned the work on request. |
| `quota_exhausted` | `KindQuotaExhausted` | A resource budget is spent. |
| `permission_denied` | `KindPermissionDenied` | The caller may not use this sidecar. |
| `model_mismatch` | `KindIntegrity` | The sidecar holds a different model than the request named. |
| `internal` | `KindInternal` | Any other sidecar-side failure. |

An unrecognized code maps to `KindInternal`. It is never guessed into a
more specific kind: a client cannot verify a claim it does not understand,
and mapping an unknown code to something retryable or to something that
blames the caller would both be inventions.

## Client failure semantics

This table is the complete list of what the client does. Nothing in it
produces a vector.

| Situation | Result |
| --- | --- |
| No sidecar listening (dial refused, socket absent) | `KindUnavailable`. |
| Connection lost mid-request or mid-response | `KindUnavailable`. |
| The call exceeded its deadline | `KindTimeout`, promptly — the stream is closed out from under the blocked read. |
| The caller canceled the context | `KindCanceled`, promptly, with no goroutine left behind. |
| Frame header truncated, payload truncated, `length` of 0, or `length` over the cap | `KindIntegrity`. |
| Payload is not decodable JSON, or not an object | `KindIntegrity`. |
| `protocol_version` absent, unparsable, or a different major | `KindUnsupported`. |
| Both `vectors` and `error` present | `KindIntegrity`. |
| `error` present | The kind its `code` maps to, per the table above. |
| Reported `model` or `dimensions` differ from the configured ones | `KindIntegrity`. |
| Vector count differs from input count | `KindIntegrity`. |
| Any vector is not exactly `dimensions` long | `KindIntegrity`. |
| Any component is NaN or infinite | `KindIntegrity`. |
| Empty input batch | An empty result and no error. No connection is opened. |

Cancellation and timeout are enforced by closing the connection, so a
sidecar that accepts a connection and then never answers cannot hold the
caller past its deadline. Because each call owns its connection, an
abandoned call leaves nothing behind that a later call could read.

## What a conforming sidecar must do

1. Accept a connection, read exactly one request frame, and enforce the
   same framing rules stated above on the way in — including the size cap,
   which protects the sidecar from a hostile client just as it protects the
   client from a hostile sidecar.
2. Refuse a request whose major protocol version it does not speak, with an
   `unsupported` error at its own version.
3. Refuse a request whose `model`/`dimensions` do not match what it loaded,
   with `model_mismatch`, rather than embedding anyway.
4. Embed every input as given, in order, and answer with one frame.
5. Never return a partial batch, a substituted vector, or a vector of a
   width other than the one it reports.
6. Close the connection after answering.
