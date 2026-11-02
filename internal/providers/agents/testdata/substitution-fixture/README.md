# substitution-fixture provenance (P1-AGT-03)

| File | What it is |
|---|---|
| `input.ndjson` | five driver stdout frames in the stream-json shape `providers/agents/claude/testdata` pins. `KEY_PLACEHOLDER` and `CANARY_PLACEHOLDER` are replaced at test time by synthetic values; no credential is committed. |
| `expected.ndjson` | the output captured from the real filter, not written by hand. |

Capture

- tool: `WrapDriverOutput` over `egress.Engine.InterceptClass(agent-driver, internal)`, the H/S-16.T1 substitution pass with the shipped detector configuration and an empty vault
- version: cascade at base commit `1adc741e` plus the P1-AGT-03 change
- date: 2026-11-02
- method: a one-off test fed `input.ndjson` line by line through the filter and wrote each returned line; the helper was deleted after the capture

Limit: the frames are synthetic, not recorded from a live vendor CLI (no key, no network
in the build). The property under test is byte absence, which does not depend on the frame
layout. `TestSubstitutionFixtureReplay` fails if the engine's output for any line moves.
