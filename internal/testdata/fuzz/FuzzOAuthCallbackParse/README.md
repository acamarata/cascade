# FuzzOAuthCallbackParse seed corpus

Seeds for `internal/secrets.FuzzOAuthCallbackParse`
(`internal/secrets/oauth_fuzz_test.go`), which fuzzes `parseOAuthCallback` —
the decoder over the loopback redirect's query string. That query is
attacker-influenced: anything that can reach the loopback port can send one,
and it is where the single-use authorization code arrives. 06-FORGE-SPEC
§5.7 requires a fuzz target over exactly this kind of input.

Ticket: P1-E08-W2-S15-T2.

Each file is one raw query string, with no leading `?`.

| File | What it covers | Expected |
|---|---|---|
| `valid-code-state.txt` | The RFC 6749 §4.1.2 success example's parameter shape | accepted |
| `missing-state.txt` | A callback with a code but no state | refused (no state means no CSRF binding) |
| `oversized-code.txt` | A 4096-character code, past `maxCallbackCodeLen` | refused |
| `percent-encoding-error.txt` | `%ZZ`, an invalid percent escape | refused |
| `duplicate-params.txt` | Two `code` parameters | refused (never "pick one") |
| `error-response.txt` | An `error=access_denied` response with a description | reported as a denial, code absent |

## Provenance

Hand-written for this ticket on 2026-09-04, with the parameter names and the
`state`/`code` example values taken from the RFC 6749 §4.1.2 and §4.1.2.1
example responses. No capture from a live provider is involved and none is
needed: these are protocol shapes, not a recorded exchange. The recorded
exchange fixture, which does describe its capture, is
`internal/secrets/testdata/pkce-exchange-fixture.json` — see
`internal/secrets/testdata/README.md`.

No file here contains a real credential.
