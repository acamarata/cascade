# cascade-pa

A builtin plugin. It owns `cascade chat` and the three MCP tools a harness
session uses to reach the operator's conversation store.

## The MCP tools a session sees

Registered by this plugin's manifest (`Provides.Tools`) and served by its
`DispatchTool`. A harness sees them only when the plugin is enabled — the
MCP surface is generated from the manifest, so a tool missing from that list
is absent from every harness no matter what the dispatcher could service.

| Tool | What it does |
|---|---|
| `cascade_cpa_send` | Records one turn, starting a thread when none is named. |
| `cascade_cpa_history` | Reads a thread's turns, or lists the threads when none is named. |
| `cascade_cpa_search` | Finds turns whose content matches a query. |

They answer the same three questions `cascade chat` asks, through the same
daemon methods, so an agent and a person see one conversation rather than
two.

### `cascade_cpa_send`

```jsonc
// in
{"content": "…", "thread_id": "…", "sensitivity": "local-only|restricted|internal|public"}
// out
{"turn_id": "…", "thread_id": "…", "created_at": "RFC3339", "sensitivity": "…"}
```

`content` is required. Omit `thread_id` to start a thread; the result names
the one the **server** minted, never the empty string the caller sent.

`sensitivity` fails closed (06 §5.16): unset, unknown, misspelled and
differently-cased all resolve to `restricted`. `"Public"` is not `public`.
The resolved tier is echoed in the result, so a caller that misspelled one
is told its request was narrowed rather than believing its public turn is
public.

**What the tier does not yet do.** It is resolved and reported; it is not
stored. `chat.append_turn` has no sensitivity field and `internal/conversation`
has no sensitivity concept, so nothing downstream reads the tier back. Recorded
as a gap against the conversation domain, not papered over here.

### `cascade_cpa_history`

```jsonc
// in
{"thread_id": "…", "limit": 20, "before_turn_id": "…"}
// out — turns when thread_id is given, thread summaries when it is not
{"items": [ … ], "next_cursor": "…"}
```

`limit` defaults to 20 and is **clamped** to 100 rather than refused: an
agent asking for a thousand turns wants as much as it can have. A zero or
negative limit is the default, because zero is what an omitted field decodes
to.

Paging runs backwards from the recent end of the thread — that is what an
empty cursor means — and each page reads oldest-first, because that is the
order a conversation reads in. An empty `next_cursor` means the start has
been reached. A cursor naming a turn the thread no longer contains returns
the newest page rather than an error, so a caller holding a stale cursor is
not stranded.

Without `thread_id` the result is the thread listing: `{id, title, updated_at}`.
`updated_at` is **derived** from the newest turn, because `chat.list_threads`
carries no modification time; it costs one extra read per thread. A thread's
only mutation is an appended turn, so that derivation is exact.

### `cascade_cpa_search`

```jsonc
// in
{"query": "…", "thread_id": "…", "limit": 10}
// out
{"results": [{"thread_id": "…", "turn_id": "…", "excerpt": "…", "score": 1.0}]}
```

A case-insensitive substring **scan**, most recent turn first, at this point
in the plan. `score` is 1.0 on every match by construction: a substring scan
has no relevance to report, and a fabricated gradient would be a ranking
nobody computed. The FTS5-backed search replaces the scan when S-44.T3 lands
and preserves this shape — which is why the field exists now.

An empty or whitespace-only query is refused. Every turn contains the empty
string, so answering would look like a working search that found the whole
store. A thread that cannot be read is skipped rather than failing the
search: one unreadable thread should not make the other nine unsearchable.

## Wiring

The tools reach the daemon through `tools.Conversations`, injected by
`internal/plugins/cascadepa_tools_wiring.go`. This package may not import
`internal/**` (Art.10.2, R-14.69), so the implementation lives at the
composition root and is handed in at startup.

Before a service is injected every tool **refuses**, naming what is missing.
It does not answer from nothing: "this process cannot reach the conversation
store" and "you have no conversations" are different facts, and an agent
handed the second when the first is true reports lost history to its
operator.

## Evidence

`plugins/cascade-pa/testdata/mcp_fixtures/` holds a complete recorded session
from a real MCP client — the client launched the server itself and called two
of these tools. See that directory's README for provenance and for why the
fixture is a capture rather than something written here.

## The Telegram bridge module (opt-in, off by default)

`plugins/cascade-pa/telegram/` is an **opt-in module**, not a feature of this
plugin. Nothing in a stock installation polls Telegram, nothing dials
`api.telegram.org`, and no document here claims otherwise. Two independent
gates have to be opened, by hand, before a single byte moves.

### Gate 1 — the module flag

The module reads a cascade-pa module manifest under the data directory:

```toml
# $CASCADE_HOME/data/plugins/cascade-pa/manifest.toml
[modules.telegram]
enabled = true
bot_token_vault_key = "cascade-pa.telegram.bot_token"   # optional; this is the default
```

With the file absent — the shipped state — the module is off and
`readTelegramModuleConfig` says so without touching anything else. There is no
environment variable and no `config.toml` key that turns it on.

### Gate 2 — the bot token, from the vault, under a standing grant

```bash
cascade vault set cascade-pa.telegram.bot_token     # paste the BotFather token
cascade vault grant cascade-pa.telegram.bot_token   # the daemon has no human to prompt
```

The token is read through `Broker.GetGranted` — the headless daemon's only
sanctioned read path (R-14.243). No grant means no token means no module. The
token itself never becomes an identity: the bridge keys everything (the durable
row, the device record, the pairing confirmation) on `SubjectFromToken`, a
SHA-256 digest of it.

### Where the module runs

**In the daemon.** `cascade daemon start` assembles the bridge, starts the
long-poll goroutine as a tracked subsystem, and stops it on the daemon's
shutdown drain. `cascade daemon status` names it (`cascade-pa.bridge`) whether it
is running, disabled or failed, so "my bot is not answering" has an answer
without reading any code.

No CLI command runs a poll loop. An earlier draft started the module from
`cascade pa pair`, whose process exits when the command returns — the long poll
died before its first 30-second `getUpdates` came back, so a `/pair <code>` typed
into Telegram reached nothing. Issuance goes the other way now: the CLI asks the
daemon.

### Pairing a Telegram account

```bash
cascade daemon start                # the bridge polls only while the daemon runs
cascade pa pair                     # asks the daemon for a code: pa.pair_code
cascade pa pair --json
```

With no daemon running the command says so (`daemon not running or unreachable`)
rather than printing a code, because a code minted outside the daemon could never
be verified: the stored digest is HMAC-SHA256 under a key derived from the bot
token, and the daemon is the only process that holds it.

Then send `/pair <code>` to the bot from Telegram. On success the bot replies
`paired: <subject>` and that Telegram user id is on the binding's allowlist.

- **One use, 10 minutes, 8 Crockford base32 characters** (R-16.37, verbatim).
- **The stored digest is keyed, not a bare hash.** 8 Crockford characters is 40
  bits, which a bare SHA-256 gives up to brute force well inside the 10-minute
  TTL; the key is derived from the bot token (HKDF-SHA256) and never persisted,
  so read access to `cascade.db` alone recovers nothing and verifies nothing.
- **A code is issued only for the bridge this daemon runs.** Name another subject
  and the command refuses by name — issuing one would look like a working command
  and fail silently in Telegram ten minutes later.
- **Five wrong codes burn the outstanding code** and record a
  `bridge.pair_lockout` event on the daemon's event journal. The counter is
  durable, so restarting the daemon does not reset it.
- **A code is redeemable only while the bot is unbound.** On a bound bot a
  non-allowlisted sender gets the same `not paired` reply whether or not the
  message is `/pair …`, and nothing is consumed or counted — a stranger can
  neither burn the owner's code nor tell a bound bot from an unbound one.
- The binding is a **paired-device record, not a node enrollment**: it invokes
  no enrollment elevation gate, carries no public key, and therefore satisfies
  no node-dispatch gate and no sync gate.

### What a paired bot can do today (and what it cannot)

Pairing works end to end. **Ordinary chat does not yet.** A message from an
allowlisted sender passes every admission gate and then reaches a placeholder
route that RECORDS it — one `bridge.message_unrouted` event on the daemon's
journal, naming W/S-48.T2 — and answers nothing. W/S-48.T2 replaces that route
with the real chat forwarder. Until it lands, do not expect a reply to anything
but `/pair`.

### What the bridge refuses

| Inbound | Answer |
|---|---|
| any update on an unbound bot | `not paired`, dropped, no handler runs |
| a sender not on the allowlist | `not paired`, dropped |
| photo, document, voice, video, sticker, location | `media not supported over the bridge`, and **never downloaded** — the module has no `getFile` method at all, and its transport refuses any Bot API method outside `getUpdates`/`sendMessage`/`answerCallbackQuery` |
| an elevated verb (`/enroll`, `/node upgrade`, …) | refused, on the text path and the callback path alike. The classification comes from the host's canonical §5.14 table, never from a list kept in the plugin |
| a replayed `update_id` | processed exactly once; the offset and a bounded window of seen ids are durable, so a restart does not replay Telegram's 24h of unacknowledged updates |
| a secret-shaped message (either transport, either direction) | refused BEFORE any pairing/binding lookup, persistence or handler dispatch — see below |

Every inbound message is stamped `Origin = bridge-telegram` and
`Untrusted = true` at the decode boundary, and there is no setter that clears
either field.

### The bridge never carries a secret, in either direction (R-21.203)

The bridge secret-reveal flow does not exist: no code path fetches a vault
value for bridge delivery, and `cascade vault get` over a bridge is not a
command. Every inbound message — text and callback data alike — passes the
H/S-16 credential-value detector before any persistence, prompt construction
or handler dispatch, and it runs BEFORE pairing/allowlist lookups too, so an
unpaired sender or a secret-shaped `/pair <token>` attempt is refused just
the same. Nothing about a refused message — not its content, not a hash, not
a fingerprint — is stored. Text about to be SENT is re-scanned before the
transport is ever called, so a credential-shaped reply is refused and
replaced with the same fixed, value-free warning. A refusal publishes one
`bridge.secret.quarantined` event carrying an opaque, random `exposure_id`
plus safe metadata (the matched credential class, which gate refused, the
chat kind, a timestamp) — never the value, a hash of it, or a vault
reference. An unwired scanner refuses every message (fail-closed default);
production always wires the real detector
(`internal/plugins/cascadepa_bridge_deps.go`). Finish the credential entry
locally: `cascade vault set`, or the local authenticated approval surface.

### Outbound

Every outbound body — replies, refusals, callback answers — crosses the
`bridge` egress class (`internal/hooks/egress`, `AllowRestricted: false`,
`AllowedTiers: {internal, public}`) before it reaches the transport, and what is
posted is the firewall's **output**. Restricted and local-only content cannot
leave over the bridge, and a stored vault value that reached a reply string is
substituted on the way out. The R-21.203 secret-value gate above runs
FIRST, on every outbound path (reply, answer, and the pairing flow's say),
before the egress firewall ever sees the text.

### Windows

Windows tier-2 has no daemon, so every Epic W bridge surface refuses with
`bridge requires the daemon (Windows tier-2)` (R-16.60a). The module makes no
"runs headless on Windows" claim.
