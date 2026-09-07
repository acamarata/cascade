# providers/openai fixture provenance (Art.2.2)

This driver's recorded-fixture tests (`TestOpenAICompatDriverRecordedFixtures`
in `openai_test.go`) replay bytes captured from real openai-compat
counterparts. Fixture bodies are embedded as Go string literals directly in
the test file next to the case that uses them (no separate `testdata/*.json`
files exist for them — `files_scope` for this ticket lists only this README
and the fuzz seed corpus below); each literal in the test file carries an
inline comment pointing back to the capture below it corresponds to.

## What was captured live, and how

Tool: `curl` (system `curl`, macOS). Date: 2026-09-06. Method: unauthenticated
`POST` to each vendor's real `/chat/completions` endpoint with a minimal
JSON body, no `Authorization` header — the response is each vendor's real
"you didn't authenticate" error path.

```
curl -s -D - https://api.openai.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'

curl -s -D - https://api.moonshot.ai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"moonshot-v1-8k","messages":[{"role":"user","content":"hi"}]}'

curl -s -D - https://api.deepseek.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}'

curl -s -D - https://api.z.ai/api/paas/v4/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-4","messages":[{"role":"user","content":"hi"}]}'
```

All four returned HTTP 401. The bodies differ in exactly the way this
driver's `extractErrorMessage`/`mapHTTPStatus` (auth.go) are built to
tolerate, which is the real, verified variance the plan's "openai/zai/
kimi/moonshot/deepseek-style" parenthetical asserts:

| Vendor              | Content-Type      | Body shape                                                              |
|---------------------|--------------------|--------------------------------------------------------------------------|
| OpenAI               | application/json   | `{"error":{"message":...,"type":"invalid_request_error","param":null,"code":null}}` |
| Moonshot/Kimi        | application/json   | `{"error":{"message":"Incorrect API key provided","type":"incorrect_api_key_error"}}` — no `param`/`code` |
| zai                  | application/json   | `{"error":{"code":"1001","message":"..."}}` — numeric-string `code`, no `type` |
| DeepSeek             | (none observed)    | plain text `Authentication Fails (governor)` — **not JSON at all** |

DeepSeek's non-JSON body is exactly why `extractErrorMessage` falls back to
the raw response body on a JSON-decode failure instead of erroring: a
decoder that assumed every vendor sends `{"error":{...}}` would be asserting
behaviour this capture disproves.

A live 429 was not observed (none of the four rate-limited an unauthenticated
single request in this session); the 429 fixture used by the recorded-fixture
test is constructed with the same OpenAI-shaped envelope as the verified 401,
changing only the status/type/message, and is labeled as constructed (not
captured) at its use site.

## What was NOT captured live, and why

A successful (HTTP 200) chat completion, a successful embeddings response,
and a live SSE stream all require a valid, billed API key. This sandbox has
no OpenAI/zai/Moonshot/DeepSeek credential authorized for this ticket, and
this ticket's own rule set forbids ever using a personal or unrelated
credential in a committed public-repo fixture. Those fixtures are therefore
**spec-derived**, not live-captured: they follow the OpenAI Chat Completions
API's publicly documented, versioned response and SSE chunk shapes exactly
(field names, nesting, the `data: {...}` / `data: [DONE]` framing), but no
live 200 response was recorded for this ticket. This is stated here plainly
rather than mislabeled as a live capture (Art.2's "never a self-authored
dialect" cuts against inventing a shape, not against being honest that a
documented, stable shape could not be captured live this run).

The tagged integration lane (`integration_test.go`,
`TestOpenAICompatDriverLiveAPI`) is the real-counterpart proof for the
success path: given a real API key via an env-ref, it drives this driver's
Chat and Stream methods against the live API end-to-end. Without a key it
reports an explicit skip reason and never silently passes.

## Fuzz corpus

`testdata/fuzz/FuzzOpenAICompatWireDecode/seed_response.json` is a Go native
fuzz corpus file (`go test fuzz v1` header — despite the `.json` extension
required by this ticket's files_scope, its content is the corpus encoding,
not raw JSON) seeded from the real captured OpenAI 401 body above, so the
very first fuzz run exercises the decoder against a real vendor wire shape
before any mutation.
