# Gateway quota replay fixture

Provenance: constructed, not captured. The wire-contract capture
(`providers/anthropic/testdata/gateway/README.md`) recorded that no live
capture exists, so no usage body or usage header set was copied from it.
`usage.json` is built by hand in the shape an endpoint usage source
returns, and `usage-source.toml` is the `[gateways.<name>.usage]` table
that decodes it.

Values:

| Field | Value | Reason |
|---|---|---|
| account | `redacted` | the scrub rule for account fields |
| used / limit | 420 / 1000 | synthetic |
| utilization | 42.5 | synthetic; differs from 100*used/limit so the test proves the reported figure wins |
| resets_at | 2026-09-06T15:00:00Z | synthetic, three hours after the test clock |

Only the endpoint source kind is implemented. The headers kind is refused
with KindUnsupported until a capture of it exists.
