# Network import policy

Network allowlist entries match one package directory exactly. No entry admits
descendants. The architecture gate rejects unlisted importers, stale entries,
missing purposes, and source that cannot be parsed, including ignored Go files.

The normative entries are:

| Packages | Purpose |
| --- | --- |
| `internal/secrets` | `oauth`, currently audit-only |
| `internal/providers/intake` | `provider-intake` |
| `providers/embeddings/bgem3`, `providers/openai`, `providers/redis`, `providers/s3`, `providers/transport` | `provider-intake` |
| `internal/plugins/remote` | `plugin-remote`, disabled by default |
| `internal/plugins/registryfetch` | `registry-fetch` |
| `internal/nodes` | `node-dispatch` |
| `plugins/cascade-pa/telegram` | `bridge` |
| `plugins/github` | `ci-poll` |
| `plugins/github/tools` | Type-only |
| `plugins/pbd`, `internal/providers/agents/egressproxy` | Process-tier-declared |

`internal/client`, `internal/daemon`, `internal/rpc`, and
`internal/fleet/sessions` appear only in `EgressNetLocalTransport`. The remaining
explicit non-egress entries are `cmd/cascade` and `internal/runtime`, which
construct local transports and socket addresses. Process-spawn lists remain
separate from network lists.

## Agent drivers

Non-test Go files under `providers/agents/` are checked by the AST scanner,
regardless of build tags. Every package under `net/` is forbidden except the
pure parsers `net/url`, `net/netip`, and `net/mail`. So are `crypto/tls`,
`syscall`, `log/syslog`, `unsafe`, `plugin`, cgo (`"C"`), and every module
outside this one, since a third-party package can dial on the driver's behalf.
An import into a `testdata` or dot directory is rejected too, in any letter
case, because the scanners skip those directories but Go compiles them when
imported. Assembly
(`.s`, `.S`) and `.syso` files are rejected. A `net` import cannot use an alias or dot import.
Its only permitted selectors are `Listener`, `Conn`, `Listen`, `UnixAddr`,
`ErrClosed`, and `OpError`. Every `net.Listen` use must be a direct call whose
first argument is the string literal `"unix"`. A variable containing `"unix"`
does not qualify. Network-capable transports belong behind the egress proxy.

## Transit and audit inventory

`AuditOnlyClasses` reports `oauth`, `spike-measurement`, and `wiki-git-push` in
sorted order. These enabled classes have no enforced transit call. The marker
reports a gap; it grants no permission. The doctor mount is owned separately.

The AST transit check reads only files that build on darwin, linux, or windows
(amd64 or arm64, with or without cgo) under this toolchain's release tags and no
custom tags, judged by `//go:build` lines and filename suffixes. It requires each other enabled class to be used in a
`Capability`, `Intercept`, or `InterceptClass` call. The sole token-based
exception is `hook.response`: dispatcher validation must reject a token of
another class, and `interceptParams` must pass that token to `Intercept`.

Registry index and artifact request URLs are admitted by shape before any
content check. A URL must use https (http only to a loopback address), have
an ASCII host with no IPv6 zone, and carry no userinfo, query or fragment. Its
path may use only letters, digits, `.`, `_`, `~`, `-` and `/`, so it holds no
percent escape, and it may not contain an empty, `.` or `..` segment, which a
server would merge or remove. An admitted URL then passes through `Intercept`
as public content, and the host (ignoring case) and the path are checked for
any stored vault value of four bytes or more, with no secret-reference tag
exemption. A URL that fails any of these is refused; it is never sent in
redacted form. Every redirect target passes the same checks before it is
requested, and at most ten redirects are followed. A refused
redirect reports its own error, never the transport's, which would quote the
refused location, and a redirect location that does not parse is reported
without its text. Missing engines, uninitialized engines, absent or
wrong-class capabilities, class refusal, and substitution errors refuse before
transport. Refusal messages never echo the URL. Response bodies reach the
caller byte-for-byte unchanged, preserving the original bytes for downstream
signature verification.
