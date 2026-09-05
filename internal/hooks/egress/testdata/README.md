# internal/hooks/egress test fixture provenance

## mcp-response-fixture.json

Shape: one tool-protocol response frame, `Response` + `toolCallResult`
from `internal/mcp/server.go`, at the pinned protocol revision
`2026-07-28`.

PROVENANCE, STATED PLAINLY: this fixture is NOT captured from a real
third-party client. It is derived from this repo's own response frame,
which is the same Art.2 deferral `internal/mcp/testdata/README.md`
already records, for the same reason and with the same risk. That file is
the canonical statement; it is not restated here, and this fixture does
not claim a provenance that file says nobody can produce today. In short:
no off-the-shelf client speaks the stateless dialect this server pins, so
"captured from a real conformant client" cannot be satisfied by any
client that exists at the time of writing.

THE RISK THIS CARRIES HERE: this fixture proves that the firewall
substitutes a credential inside a payload of this SHAPE. It does not
prove the shape is what a real client would send. If the real frame
layout differs, the substitution still runs, because the firewall treats
the payload as opaque bytes and never parses it. So the risk this
deferral carries for the egress tests is smaller than it is for the
protocol tests: the property under test here is byte absence, which does
not depend on the frame being correct.

The literal `SECRET_PLACEHOLDER` is replaced by the test's own synthetic
secret at run time. No real credential is committed, and none ever should
be: a fixture holding a live value would make this directory the leak it
exists to prevent.

## hook-response-fixture.json

Shape: the hook engine's own audit record (`HookFire` in
`internal/hooks/hooks.go`) plus the `action_params` bag the dispatcher
hands across the plugin seam. It is an INTERNAL-shape fixture: the struct
is owned by this repo, so there is no external counterpart to capture it
from, and none is claimed.

The same `SECRET_PLACEHOLDER` substitution at run time applies.
