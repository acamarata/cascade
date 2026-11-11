# P1-CPA-13 chat reply contract, proof and limits

## Contract

P1-CPA-13 requires completing the composition and client surface of the
P1-CPA-16 Replier. The required sequence is `chat.append_turn` → `chat.reply_open`
→ `chat.reply {stream_id}` → `chat.get_thread`. One-shot output must come
from the durable assistant turn on the same thread as the user turn.
The client reply timeout is ten minutes. TUI streaming subscribes to
`GET /events?topic=chat.reply&stream=<stream_id>` before dispatch, renders
deltas, checks the terminal frame and RPC result, then reads durable state.
The shipped client uses the supported `stream_id` query alias.

Required daemon composition uses `daemon.ProcessConductorDoor()`, the
P1-CPA-08 journal, `openEffectLog` and the same ReplyHub as the SSE topic.
`RecoverPending` must finish before reply methods register. Stored replies
are reused on a newly opened stream without another model call. In-flight
replies to the same user turn conflict. Unfinished effects at restart become
unknown outcome rather than confirmed success.

Policy denial of `chat` and quota exhaustion must return `KindPolicyDenied`
and `KindQuotaExhausted` with zero provider calls through real configuration.
Local-only restrictions cover classification, summarization and generation;
scrub and privacy checks precede egress. Cancellation after the first token
must return a typed failure, persist no assistant turn and print no success.
Windows remains tier-2: daemon-required one-shot and TUI paths refuse.

The `chat.reply` SSE topic is owner-view and consumes the SEC-18 visibility
predicate. Delivery requires both the stream's caller binding and the
predicate, checked before the handshake and per frame. Another caller's
stream is HTTP 403 with zero frames. Principals default to deny-all absent
an admitting ancestry predicate. Stream ids are single-use for generation,
expire after 60 seconds unclaimed, and retain terminal buffers for 60 seconds.
Frames are `delta`, `done` or `error`, always with `thread_id`; `delta`
carries text and `error` carries only a kind. Field renames require C7.

See the [CLI reference](../../../docs/cli-reference/chat.md) and
[RPC protocol](../../../docs/rpc-protocol.md) for the wire shapes.

## Proof present in the tree

| Evidence | What it establishes | Limit |
| --- | --- | --- |
| `internal/plugins/cascadepa_wiring.go` and adjacent tests | Append/open/reply/readback client flow, subscription before dispatch, ten-minute timeout, errors and cancellation | Does not establish daemon production composition |
| `cmd/cascade/wire_chat_reply_test.go`: `TestReplyStreamServedOverRealSSEHandler`, `TestReplyTopicRefusesOtherCallersStream`, `TestP1ChatReplyStreamEvents` | Explicitly composed Replier and real RPC/SSE handler; deltas, terminal frame, durable readback, binding and visibility refusal | Uses an injected scripted door, not the production conductor |
| `cmd/cascade/chat_reply_integration_test.go`: `TestP1ChatReplyRoundTrip`, `TestP1ChatReplyCancelAfterFirstToken` | Under `integration && !windows`, socket reconnect finds both turns; cancellation yields a typed failure, terminal error and only the user turn | Explicit test composition; missing recording falls back to inline text |
| `cmd/cascade/chat_reply_windows_test.go`: `TestChatReplyWindowsTierTwo` | Windows-tagged daemon start/run tier-2 refusals | Does not invoke `cascade chat -m` |
| `cmd/cascade/chat_reply_live_test.go`: `TestP1ChatReplyLive` | Live-test implementation gated by `live && !windows` and `CASCADE_LIVE_PROVIDER_SMOKE=1`; enabled runs fail on a missing key or failed exchange | Test source alone is not evidence of a successful live run |

The required documentation-ticket check is:

```sh
env -u TMPDIR go test -count=1 ./cmd/cascade/
```

That plain check does not run integration, live or Windows-tagged tests.
A passing result cannot establish the production or live claims below.

## Limits and split reopening

The supplied premise that P1-CPA-13a..e are complete contradicts this tree.
These are outstanding acceptance requirements, not completed proof:

- **P1-CPA-13b:** `wireChatHandlers` discards the journal handle returned by
  `wireChatJournal` and never calls `wireChatReply`. The helper exists but
  production does not compose it with the process conductor, effect log and
  topic hub. Normal daemon requests to the reply methods therefore fail as
  method-not-found after the client has recorded its user turn. The
  `cmd/cascade/testdata/daemon_rpc_methods.golden` inventory also lacks
  `chat.reply_open` and `chat.reply`.
- **P1-CPA-13b:** `TestChatReplyWiredInDaemon`,
  `TestP1ChatReplyProductionPath`, `TestP1ChatReplyOwnerDenyZeroCalls`,
  `TestP1ChatReplyQuotaZeroCalls`, `TestP1ChatReplyLocalOnlyThreadNoRemoteCall`
  and `TestP1ChatReplyScrubBeforeEgress` contain logging only, with no
  behavioral assertions. Their passing status proves neither wiring nor
  provider-call counts, over-budget privacy enforcement or secret scrubbing.
- **P1-CPA-13d:** the recorded exchange fixture (recorded_exchange.json in
  the chat_reply testdata folder of the cascade command) and its provenance
  README are absent. No successful live capture is
  established here. The live capture shape records prompt/reply and method
  names, but does not record the required streamed frames, provider-error
  and cancel-after-first-token cases. Those cases and the required tool,
  version, date and `captured from a live call: yes` provenance remain owed.
- **P1-CPA-13e:** integration replay silently uses inline text when the live
  recording is absent. It cannot currently prove replay of a captured live
  exchange. Its cancellation test exercises the RPC client and stored state,
  not CLI exit status or printed output. The Windows test likewise leaves
  the one-shot command-level refusal unproved.
- **P1-CPA-13a:** the streaming client wraps a terminal error frame as
  `KindUnavailable`, including when the server frame names another kind.
  One-shot RPC failures retain typed kinds, but stream error typing depends
  on which error path arrives first. Preserve typed refusal semantics before
  claiming parity across client modes.

Cancellation during generation drops partial text. A late cancellation
after generation cannot roll back a durable write: persistence uses a
context without cancellation. Similarly, an effect confirmation failure can
return an error after the assistant turn is stored. Clients must read durable
state rather than infer absence from every failure. The SSE buffer is
process memory, not a restart-safe log or a cursor-based resume surface.

This documentation change makes no code or test repair and does not close
the parent production, live-provider, mutation or review gates. Reopen the
owning splits above before reporting P1-CPA-13 complete.
