# internal/runtime/init/testdata/accept provenance

Fixtures for `P1-E16-W4-S35-T5`'s scenario D and its error path: the
responses a provider endpoint gives during `cascade provider add`, replayed
by an `httptest` server the setup file points at through `base_url`.

## provider-verify/models.json
Tool: constructed by hand from the OpenAI-compatible `GET /v1/models` list
response shape (`{"object":"list","data":[{"id":...,"object":"model",
"created":...,"owned_by":...}]}`), the shape shared by every
openai-compat driver per 08-INIT-CONFIG-SPEC.md §2. This is the same shape
`internal/providers/intake/testdata/probe_openai_compat.golden.json`
carries; it is repeated here rather than imported because this suite drives
the real binary as a child process and cannot reach another package's
testdata.
Date recorded: 2026-09-17.

## provider-verify/completion-ok.json
Tool: constructed by hand from the OpenAI-compatible
`POST /v1/chat/completions` response shape, with the one-token body the
micro-verify actually sends (`max_tokens: 1`), so `finish_reason` is
`length` rather than `stop` — which is what a real endpoint returns for
that request, and the detail a shape invented from the general reference
would get wrong.
Date recorded: 2026-09-17.

## provider-verify/completion-401.json
Tool: constructed by hand from the OpenAI-compatible error envelope
(`{"error":{"message":...,"type":...,"param":...,"code":...}}`) as returned
for a rejected credential, served with HTTP 401.
Date recorded: 2026-09-17.

## Honest limitation
Art.2 calls for real-counterpart fixtures with stated provenance. This
build environment has no permitted network egress for fixture recording,
and a genuine success capture would additionally require a live vendor
credential. These three are therefore hand-built from the published
API reference, exactly as
`internal/providers/intake/testdata/README.md` records for the three probe
goldens beside them, and for the same reason. That is stated here rather
than asserted as a live capture it is not.

What the suite still proves against the real thing: the request the binary
SENDS is the real one. The fixture server asserts on method, path, the
Authorization header and the request body, so a change to what cascade
sends fails these scenarios even though the response is constructed. The
half that is a stand-in is the response shape, and the half that is real is
cascade's own behaviour — which is the half an acceptance suite is for.

## provider-verify/anthropic-models.json, anthropic-message-ok.json, anthropic-401.json
Tool: constructed by hand from the Anthropic Messages API reference — the
`GET /v1/models` list shape (`{"data":[{"id":...,"type":"model",...}],
"has_more":false,...}`), the `POST /v1/messages` response, and the
authentication-error envelope, served with HTTP 401.
`stop_reason` is `max_tokens` rather than `end_turn` because the
micro-verify sends `max_tokens: 1`, which is what a real endpoint returns
for that request.
Date recorded: 2026-09-17.

Why both shapes are here: the shape probe, not the setup file, decides
which driver a provider gets — see the hazard test
`TestAcceptInitTheSetupFileKindIsIgnored`, which pins that
`P1-E16-W4-S35-T12` has not landed yet. Scenario D therefore drives the
shape the probe actually selects, and the openai-compat fixtures above
back the hazard test that records the defect.
