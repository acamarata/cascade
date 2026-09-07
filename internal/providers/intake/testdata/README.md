# internal/providers/intake/testdata provenance

## probe_anthropic.golden.json
Tool: constructed by hand from the Anthropic Messages API reference,
`GET /v1/models` response shape (`{"data":[{"id":...,"type":"model",...}],
"has_more":false,"first_id":...,"last_id":...}`). Not a live capture: this
build environment has no network egress permitted for fixture recording.
Version: shape as documented 2026 (`anthropic-version: 2023-06-01`).
Date recorded: 2026-09-06.

## probe_openai_compat.golden.json
Tool: constructed by hand from the OpenAI-compatible `/v1/models` list
response shape (`{"object":"list","data":[{"id":...,"object":"model",
"created":...,"owned_by":...}]}`), the shape shared by OpenAI, zai,
Kimi/Moonshot and DeepSeek per 08-INIT-CONFIG-SPEC.md §2. Not a live
capture, for the reason stated above.
Date recorded: 2026-09-06.

## probe_gemini.golden.json
Tool: constructed by hand from the Gemini `models.list` response shape
(`{"models":[{"name":"models/...","displayName":...,
"supportedGenerationMethods":[...]}]}`). Not a live capture, for the
reason stated above.
Date recorded: 2026-09-06.

## fuzz/FuzzShapeProbeResponse/seed_probe.json
One seed corpus entry: the raw bytes of probe_anthropic.golden.json,
exercising modelsFromProbe's anthropic decode path as the fuzz target's
starting corpus (R-21.266: one package-local corpus per fuzz check).

## Honest limitation
Art.2 calls for "real-counterpart" fixtures with stated provenance. This
sandbox has no permitted network egress to record a genuine live capture
against any vendor endpoint, so these three fixtures are hand-built from
each vendor's published API reference rather than a live response. This is
recorded here rather than asserted as a live capture it is not. A follow-up
pass with network access should replace these with genuine captures and
update this file's dates.
