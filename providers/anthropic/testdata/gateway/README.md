# Gateway wire-contract fixtures

Status: no live capture exists. This directory holds no captured fixtures.

## Finding (recorded 2026-10-12)

The capture was meant to probe one operator-run Anthropic-compatible gateway
within a bounded request budget and keep scrubbed byte captures here. The
live leg could not run:

| Check | Observed |
|---|---|
| Gateway host DNS | NXDOMAIN; the zone has no delegation, and other hosts resolve, so the resolver is healthy |
| Credential in the OS keychain | item not found |
| Credential key-file fallback | absent |
| Credential helper | exits non-zero and writes 0 bytes |
| Desktop gateway profiles | removed from the machine |
| Requests sent | 1, a local DNS failure; it carried no auth header and reached no upstream |

An endpoint failure is a recorded finding, not a blocker. No gateway, credential
or spend was requested to work around it.

## Consequences

- The model listing, the non-streaming and streaming message shapes, the
  accepted auth header, the 401 body and the transient-failure shapes were not
  captured. Nothing here claims them.
- Consumers of this contract build constructed fixtures. Each one is labelled
  `constructed` and cites the public API reference, not a capture. Values use
  synthetic placeholders (`model-a`, `msg_000`, `gateway.invalid`, `redacted`).
- A live leg in any consumer records this same finding until an operator names
  a reachable gateway and the capture is redone.

## What did land

The identifier sweep now scans testdata and never prints a match, so a captured
fixture cannot reintroduce a private identifier unnoticed (see
`docs/developer/hygiene.md`).
