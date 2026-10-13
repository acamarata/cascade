# CC channel fixtures — provenance (P1-E37-W8-S73-T1)

Per the D/S-06.T6 lesson (a ruling's paraphrase of an external protocol is not the protocol), this
records exactly what was captured, how, and what was verified true or false by live testing rather
than assumed. **Correction (2026-09-23):** an earlier version of this file claimed no real
"claude/channel" capability exists. That claim was wrong and has been replaced below after a
reviewer pointed to the real feature and the capture was repeated live.

## Tool / version / date / method

- Tool: the CC harness CLI (`claude`), version `2.1.273` (`claude --version`, captured live).
- Date: 2026-09-23.
- Method: (1) `claude --help` (full output logged, no `--channels` entry — the flag is real but
  undocumented there); (2) a deliberately invalid `--channels` value to elicit the harness's own
  usage message, which named the real syntax; (3) a throwaway stdio MCP server under `/tmp`
  (never committed) declaring `experimental["claude/channel"]`, driven against the harness via
  `--mcp-config <file> --channels server:<name> --strict-mcp-config`, run three times (one
  single-shot `-p`, one `-p --output-format stream-json --verbose`, one `-p --input-format
  stream-json --output-format stream-json` two-turn); (4) reading the real, currently-installed,
  currently-running official "telegram" channel plugin's `server.ts` (the harness's official plugin
  marketplace, installed locally) as the reference implementation of the same capability.

## Verified real facts

1. **`--channels` is a real flag**, hidden from `--help` but enforced by real argument validation.
   Passing an untagged value produced: `--channels entries must be tagged: <value>` and named two
   real forms — `plugin:<name>@<marketplace>` (plugin-provided, allowlist enforced) and
   `server:<name>` (a manually configured MCP server). No `--dangerously-load-development-channels`
   flag was found in `--help` or referenced by the usage error; that part of the tip could not be
   confirmed and is not asserted.
2. **The capability and wire method are real**: an MCP server declares
   `capabilities.experimental["claude/channel"]` (and, for tool-permission relay,
   `"claude/channel/permission"`) in its `initialize` response, and sends
   `{"method":"notifications/claude/channel","params":{"content":"...","meta":{...}}}` as a
   server-to-client notification. This is not this session's inference — it is read directly out
   of the real, installed, currently-running official telegram plugin's source (method name and
   `{content, meta}` shape), which is Anthropic-distributed reference code, not a fixture this
   session authored.
3. **This session's own throwaway server was accepted**: the harness's real `system`/`init` event
   reported `"mcp_servers":[{"name":"cascadetest","status":"connected"}]` after negotiating
   `protocolVersion":"2025-11-25"` with the real `clientInfo` shown in `protocol-negotiation.json`.
4. **Delivery into a `-p`-driven session was tested three times live and did not occur.** The
   real notification bytes were sent (server debug log timestamps confirm the write happened
   before each turn's assistant reply); the assistant's own answer, asked directly to report any
   received channel content, was "NONE" / made no mention of it in all three runs. This matches
   how `providers/agents/claude` in this repo actually drives the harness for an "active session
   or task" — always via `-p` (see `providers/agents/claude/descriptor.go`'s `ProbeArgv`, `-p
   --output-format stream-json`). The real, running telegram plugin proves the same capability DOES
   deliver for its own use case, but that daemon runs the harness fully interactively (no `-p`,
   a long-lived tmux-attached session) — a materially different delivery target than this
   ticket's dependency-confirmed "active session or task" is built against.

## Disposition

`addressed-inbox-delivery.json` and `protocol-negotiation.json` are present, built from the real
captures and real reference source above (see each file's own `_provenance`/`honest_gap` fields
for exactly what is directly captured vs. cited). No self-authored dialect was substituted for
either the capability declaration or the notification method name/shape — both are the real ones.

The production probe in `cc_channel.go` (`indeterminateProbe`) therefore reports
**indeterminate**, which resolves to unsupported: the capability exists and connects, but a server
has no observable proof that a pushed notification reaches a `-p`-driven session (the delivery
target cascade uses for an active CC session or task). The push path stays off under the Art.6
deferral and every addressed event is delivered by next-turn hydration. Nothing outside the package
can make the probe report supported.

A fully interactive (PTY-attached, no `-p`) capture — the one delivery mode this session's evidence
suggests would actually succeed, per the real telegram daemon's own proof of working delivery —
was not attempted this pass; scripting a PTY-driven multi-turn interactive capture was judged too
large an addition for this evidence pass and is named as a follow-up, not silently skipped.

## Neutralised fields (identifier sweep)

- `protocol-negotiation.json`: the captured `clientInfo` named the vendor product in its `name`,
  `title`, `description` and `websiteUrl` fields. `name` is replaced by the neutral token
  `cc-harness`, `title` by `<redacted>`, and `description` and `websiteUrl` are removed. The
  harness-version key of the `system`/`init` excerpt is renamed `harness_version`; its value is as
  captured. `protocolVersion` (`2025-11-25`), `capabilities` and `version` (`2.1.273`) are exactly as
  captured: they are the fields `cc_channel.go`'s negotiation reads.
- `addressed-inbox-delivery.json`: no local install path appears; the reference plugin is cited by
  file name and marketplace only.
- No account path, lane identifier or personal config value appears in either fixture, and the vendor
  product name is written only as "the CC harness" in prose.

## How the fixtures are used

- `cc_channel_test.go` replays `request.params` of `protocol-negotiation.json` as the harness's
  handshake frame: MCP `protocolVersion` `2025-11-25` maps to channel revision 1 (the declared range
  min=1, max=1), and any other value is refused at runtime with `provider.ErrHarnessIncompatible`.
- The same test compares every frame the channel sends with the `A_sent_by_this_session`
  notification of `addressed-inbox-delivery.json`: method `notifications/claude/channel`, params
  `{content, meta}` with string-valued meta.
- `testdata/fuzz/FuzzCCChannelDecode/valid.json` is the captured `request.params` with the same
  neutralised `clientInfo`.
