# Driver egress proxy

How an external agent driver's network traffic is routed, what the proxy
refuses, and what it cannot stop. Read the limits section before relying on
any of it.

## What it is

Every external driver spawn gets its own HTTP CONNECT proxy, bound to
`127.0.0.1` on an ephemeral port and owned by that spawn. The package is
`internal/providers/agents/egressproxy`. Nothing in it is process-wide: two
spawns get two proxies, two ports and two credentials.

The proxy admits a tunnel only to a destination that is an exact member of
that driver's allow list. It copies the tunneled bytes without reading,
logging or journaling them. Most driver traffic is TLS, so the proxy sees
the destination authority and nothing inside the tunnel.

## Limits: advisory routing, not containment

The proxy is advisory. The driver reaches it only because the spawn
environment points it there, and a driver can ignore that environment and
open a direct connection. The proxy does not firewall the driver's egress
and does not inspect it. OS-enforced containment (a per-driver network
namespace) is the deferred item `DEF-P2-driver-netns`.

What the proxy does give:

- a harness that honours its proxy settings reaches only the listed
  destinations;
- every admitted and refused connection is journaled, so traffic that went
  through the proxy can be attributed to a driver and a job;
- an inherited bypass cannot survive, because the spawn environment sets
  `NO_PROXY` and `no_proxy` to empty instead of inheriting them.

If a supported harness is shown to ignore `HTTPS_PROXY` or the proxy
credential for its vendor traffic, the proxy is not a control for that
harness, and this page must say so by name.

## Configuration

The allow lists are cold, file-only config. There is no `cascade config set`
path for them.

```toml
[agents.egress.claude]
allow = ["api.example.test:443"]

[agents.egress.codex]
allow = ["api.example.test:443", "uploads.example.test:443"]
```

- The table name is one of the closed driver ids: `claude`, `codex`,
  `opencode`, `antigravity`. Any other id is refused.
- The only key is `allow`. Any other key is refused.
- Each entry is `host:port`. The host is a lowercase ASCII DNS name, an IPv4
  literal, or a bracketed IPv6 literal with no zone. The port is decimal,
  1 to 65535, with no leading zero.
- Refused entries: wildcards (`*.example.test:443`), schemes, paths,
  userinfo, a trailing dot, a missing port, upper case, and numeric
  shorthands such as `127.1` that a resolver may read as an address.
- A missing table, or an empty list, denies every destination.

Every refusal is an invalid-input error naming the offending key, for
example `agents.egress.codex.allow[1]`.

Membership is exact string equality after normalization. The proxy never
resolves a name to compare addresses, never matches by suffix or CIDR, and
carries no built-in vendor host table. An IP literal of a listed name is a
different destination and is refused.

## Credential

Any local process, under any user, can connect to a loopback port. So each
proxy mints a random per-spawn credential and demands it on every request in
a `Proxy-Authorization: Basic` header. The proxy URL in the driver
environment carries it as the password for user `cascade`. A missing
credential, or another spawn's credential, gets 407 and no dial.

The spawn environment holds exactly six proxy variables: `HTTPS_PROXY`,
`HTTP_PROXY`, `https_proxy` and `http_proxy` set to the proxy URL, and
`NO_PROXY` and `no_proxy` set empty. Both cases are set because some clients
read only the lowercase names.

## Request handling

One request per connection, decided in this order:

| Step | Check | Refusal |
|---|---|---|
| a | Proxy credential present and correct | 407 `proxy-auth-required` |
| b | Method is CONNECT | 403 `method-not-connect` |
| c | Target is a valid `host:port` authority | 400 `malformed-destination` |
| d | Target is an exact allow-list member | 403 `destination-not-allowlisted` |
| e | Fewer than 64 tunnels open | 503 `tunnel-limit` |
| f | Intent row journaled before the dial | 403 `journal-unavailable` |
| g | Upstream dial within 10 s | 502 `dial-failed`, or 403 `resolved-address-forbidden` |
| h | Confirm row journaled, then `200 Connection Established` | 403 `journal-unavailable` |

The `Host` header is ignored; only the request target counts. Absolute-form
targets, IPv6 zones and zero-padded ports are malformed. Host names are
lowercased before the membership check, and a target with any non-ASCII byte
is malformed. The request head must arrive within 10 seconds and within
32 KiB. Every refusal closes the connection.

The production dialer checks the address it is about to connect to, after
resolution and before the connect call. Loopback, unspecified, link-local,
multicast, private (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`,
`fc00::/7`), shared (`100.64.0.0/10`), reserved, and NAT64 or 6to4
addresses are refused. That stops a listed name that resolves, or is
rebound, into the local network.

## Journal

Every decision is one row: driver id, job id, destination, phase, allowed,
reason. The destination is empty whenever the target did not parse; a raw
request target is never journaled.

| Phase | Written |
|---|---|
| `decide` | a refusal before any dial |
| `intent` | before the dial; if it cannot be written, there is no dial |
| `confirm` | after the dial: `connected`, `dial-failed` or `resolved-address-forbidden`, and `closed` when the tunnel ends |

A journal error, or a journal panic, on a refusal still refuses. The spawn
shim binds the journal to the redacting audit log as a policy decision with
actor `agent-driver:<driver-id>`, action `agent.egress.connect`, verdict
allow or deny, and the reason as the outcome.

## Lifecycle

Closing the proxy closes the listener first, synchronously, then every open
tunnel. Cancelling the spawn's context closes it the same way. After close,
a connection to the old port is refused and an open tunnel reads end of
file.
