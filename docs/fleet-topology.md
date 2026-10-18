# Fleet topology

Reference for the fleet topology package (`internal/fleet/topology`): the
accounts, quota domains, runtime profiles, credentials and lanes the
conductor routes over. This page covers the runtime-profile additions.

## Runtime profiles

A runtime profile is one way to run a model on this machine or elsewhere
(`claude-cli`, `codex`, `api`, `ollama` and so on). Each account's persistent
profile keeps its own credentials and config in a private home directory, so
two accounts never read or overwrite each other's files.

### Home layout and modes

The home of profile `<id>` is `$CASCADE_HOME/profiles/<id>/`.

| Rule | Detail |
|---|---|
| Id shape | `^[a-z0-9][a-z0-9._-]{0,63}$`, no `..`, no trailing `.`, and no Windows reserved device name (`con`, `prn`, `aux`, `nul`, `com0`-`com9`, `lpt0`-`lpt9`, with or without an extension). Anything else is refused with `ErrTopologyInvariant` naming the id. The root must be an absolute path. |
| Mode (unix) | `EnsureHome` creates `profiles/` and the home `0700`. Both are opened without following links, must be directories owned by the effective user, and are tightened to `0700` through the open descriptor, then verified. A second call changes nothing and returns the same path. |
| Refusals | A file or symlink at the home path or at `profiles/`, a directory owned by another user, or a stat or chmod failure, returns `ErrProfileHomePermission` and no path. |
| Windows | Tier-2 declared refusal. Windows has no `0700`, so `EnsureHome` returns an existing real directory and refuses a missing path, a file, a symlink or a reparse point. It never creates one. |

`HomePath` does no filesystem access. Passing the home to agent spawns is a
separate step owned by the spawn path, not by this package.

### Endpoint records

`SetEndpoint` records a network endpoint on a persistent profile only. A
non-persistent profile returns `ErrTopologyInvariant` and its row stays
unchanged; an unknown id returns `ErrTopologyNotFound`; a database failure
returns an unavailable error, never a silent no-op. An empty endpoint and a
`unix:<path>` endpoint carry no port; any other endpoint needs a port from 1
to 65535. A `unix:` host must be an absolute socket path with no whitespace or
NUL (`unix:` alone, `unix:x.sock` and `unix://evil.com/x` are refused). A
network host holds no `:` unless it is a bare IPv6 literal, so a `host:port`
string cannot be stored in the host column.

`SetRuntimeConfig` records the config-home environment variable and the
config directory. The directory must lie inside the profile's own home after
symlinks are resolved. A path outside it, a `..` escape, a symlink pointing
out, a link that does not resolve yet (a dangling link is not a directory still to
be created), a relative path, a symlink or file at the home path itself, a
symlinked or non-directory `profiles/`, an environment variable name that
is not portable (letters, digits and underscore, no leading digit), or only
one of the two values is refused and the row is left unchanged.

### The locality rule

One classifier decides locality: `topology.URLIsLocal(baseURL)`. The router's
local-only filter (`computedLocalityIsLocal` in the conductor) calls it on a
provider's base URL, and `EndpointIsLocal(profile)` calls it on the profile's
endpoint, so the lane ceiling and the router table cannot disagree.

A base URL is local only when it is:

- a unix socket: `unix://<abs path>` or `unix:<abs path>`, with a path that is
  already clean (no `//`, `.` or `..` segments, no trailing `/`), is more than
  `/`, and holds no Unicode whitespace or control character even after
  percent-decoding, and with no query or fragment. A unix URL with an
  authority (`unix://api.evil.com/x`, `unix://localhost/x`, userinfo as in
  `unix://@/x`) or a relative path (`unix:x.sock`) is remote; or
- a URL with a scheme whose host, in its canonical spelling, is `localhost`,
  `[::1]` or a literal `127.x.y.z` with four canonical decimal octets (no
  leading zeros, each 0-255). A port, when `:` is present, must be a canonical
  decimal from 0 to 65535 (`:00080`, `:65536` and an empty `:` are refused).

Canonical spellings only: a spelling Go's HTTP proxy selection would not
treat as loopback is remote. `http://127.000.000.001`, an unbracketed `::1`
and `LOCALHOST` would go to `HTTP_PROXY`, off this machine. Some spellings Go
does send direct (`http://[::ffff:127.0.0.1]`, `http://[0:0:0:0:0:0:0:1]`)
are refused too, which is the safe direction. The host is never
lowercased, because Go's proxy rule skips only the lowercase `localhost`.
Other non-canonical spellings (`http://127.0.0.1:`, a padded port, a URL with
no scheme) are refused the same way even where Go would still connect
directly.

An endpoint host is classified as the URL the router would see: a `unix:`
value as is, any other host as `http://<host>`, with an IPv6 literal
bracketed (`::1` becomes `http://[::1]`). The wrapped URL must parse back to
exactly that host, so an endpoint host carrying a path, query, fragment or
userinfo (`127.0.0.1#x`, `127.0.0.1@evil.com`) is remote, and `SetEndpoint`
refuses to store it. `SetEndpoint` accepts a `unix:` host only when
`URLIsLocal` reads it as a socket.

Against the router body this replaced, the classifier changes these inputs:

| Input | Before | Now | Why |
|---|---|---|---|
| `unix://` (no path), `unix:///`, `unix:///.`, `unix:////` (root after cleaning) | local | remote | no socket is named |
| `unix:///run/a b.sock`, `unix:///run/a%20b.sock`, `unix:/run/a%0bb.sock`, `unix:/run/a%C2%A0b.sock`, `unix:///%00` (Unicode whitespace or a control character, also percent-encoded) | local | remote | a malformed value is remote, never local |
| `unix:////run/x.sock`, `unix:///run/../x.sock`, `unix:///run/x/` (path not clean) | local | remote | canonical spellings only |
| `unix://@/x`, `unix://user@/x` (userinfo), `unix://?q`, `unix:///x#f` (query or fragment) | local | remote | an authority or a non-path part is not a socket path |
| `http://127.0.0.1:00080`, `http://127.0.0.1:65536` (padded or out-of-range port), `//127.0.0.1` (no scheme) | local | remote | canonical spellings only; nothing usable is lost |
| `unix:/<abs path>` (single colon) | remote | local | no authority, so it can only name a file on this machine, like `unix:///<abs path>`; the ticket requires `unix:/path` endpoints to be local through one predicate |

**An empty endpoint is remote on every profile.** A spawned CLI runtime
declares no endpoint and sends every prompt to its vendor, so treating it as
local would admit local-only content to a remote service. A runtime that
truly runs on this machine declares its loopback endpoint. Locality is never
read from the runtime kind, the profile id or a provider name.

| Endpoint host | Local |
|---|---|
| `127.0.0.1`, `127.1.2.3`, `::1`, `localhost`, `unix:/run/x.sock`, `unix:///run/x.sock` | yes |
| `127.evil.com`, `127.0.0.01`, `127.1`, `LOCALHOST`, `[::1]`, `10.0.0.5`, `192.168.1.2`, `host.invalid`, `unix:x.sock`, `unix://localhost/x`, empty | no |
