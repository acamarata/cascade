# Provider guide — OAuth

How a provider driver gets a bearer token from cascade, and what cascade does
with that token on the driver's behalf.

The contract lives in `pkg/provider` (`oauth_types.go`), so a driver under
`providers/` or a plugin under `plugins/` can consume it without importing
`internal/`. The implementation lives in `internal/secrets`.

Ticket: P1-E08-W2-S15-T2. Related: `docs/security-posture.md`,
`docs/elevation.md`.

## The one-paragraph version

A driver declares a `ProviderOAuthConfig`. `internal/secrets.OAuthBroker` runs
the RFC 7636 authorization-code-with-PKCE flow against it: it binds an
ephemeral loopback port, sends the operator's browser to the authorization
endpoint with a fresh S256 challenge and a single-use `state`, waits for the
redirect, exchanges the code, and stores the tokens as ordinary vault entries.
The driver never sees a `state`, a verifier, or a stored token unless it asks
for one, and what it gets back from `Start` and `Refresh` is a `TokenRecord`
that names two vault keys and *cannot* hold a token.

## `ProviderOAuthConfig`

| Field | Meaning | Refused if |
|---|---|---|
| `ProviderID` | Names the provider. Becomes part of every vault key. | Empty, or outside the vault name charset (letters, digits, `_`, `-`, `.`), or starts with `-`. |
| `ClientID` | The public OAuth client id. | Empty. PKCE clients have no client secret, and cascade has nowhere to put one. |
| `Scopes` | Scopes requested at authorization time. | — |
| `RedirectURI` | The loopback redirect target, **without a port**. | Anything that is not `http://127.0.0.1/...`; a fixed port; a query or fragment. |
| `AuthEndpoint` | The authorization endpoint. | Not `https`, unless it is `http` on `127.0.0.1`. |
| `TokenEndpoint` | The token endpoint. | Same rule. |
| `RevocationEndpoint` | Optional RFC 7009 endpoint. | Same rule when set. |
| `PKCEMethod` | Must be `PKCEMethodS256`. | Anything else, `"plain"` included. |
| `NoBrowser` | Print the authorization URL instead of launching a browser. | — |

Three of those refusals are worth their own sentence, because each one is a
real attack and not a style preference.

**`localhost` is not accepted, `127.0.0.1` is.** `localhost` is a name. A
hosts-file entry or a poisoned resolver can point it somewhere that is not this
machine, and whatever answers there receives the authorization code.

**The redirect URI must not fix a port.** The broker binds `127.0.0.1:0` and
substitutes the port the OS assigns (`RedirectURIForPort`). A hard-coded port
can be squatted by any other local process, and a squatted callback port *is*
the authorization code.

**`plain` PKCE is not offered.** It sends the verifier as its own challenge,
which is a no-op against anyone who can read the authorization request — on a
shared machine, that is the browser history and the process table.

## `OAuthBroker`

```go
type OAuthBroker interface {
    Start(ctx context.Context) (TokenRecord, error)
    Refresh(ctx context.Context, account string) (TokenRecord, error)
    Revoke(ctx context.Context, account string) error
}
```

`internal/secrets.NewOAuthBroker(cfg, deps)` returns the production
implementation. `OAuthDeps` needs a vault `*Broker` and a `Clock`; the entropy
source, the diagnostics writer, the 5-minute deadline, the expiry skew, and the
environment reader all have production defaults.

`AccessToken(ctx, account)` is the accessor a driver actually calls per
request: it returns usable bytes, refreshing first if the stored record has
expired.

### `Start`

1. Refuses immediately with `ErrNoInput` when `CASCADE_NO_INPUT=1` — before a
   port is bound and before a goroutine is started. A non-interactive run fails
   in milliseconds rather than hanging for five minutes on a browser that will
   never open.
2. Binds `127.0.0.1:0` and derives the real redirect URI from the assigned port.
3. Mints a fresh PKCE verifier and a fresh `state`, both 32 random bytes.
4. Opens the browser, or prints the URL when `NoBrowser` is set. A browser that
   fails to launch is not fatal: the URL is printed and the listener keeps
   waiting.
5. Waits up to five minutes, then closes the listener and returns
   `ErrCallbackTimeout`. Cancelling the parent context does the same.
6. Validates the callback, exchanges the code, stores the tokens.

### `Refresh` and the 401 path

`Refresh` is single-flight, keyed by account. Ten concurrent 401s produce one
outbound refresh request and ten callers holding its result. This is
correctness rather than efficiency: against a provider that rotates its refresh
token on every use, ten concurrent refreshes invalidate each other.

When the provider answers `invalid_grant`, the grant is gone. Cascade
**purges** the stored credential and returns `ErrGrantRevoked`, so no later
call can serve a token whose grant the provider has already dropped.

## What is stored, and where

Nothing new. Tokens are ordinary entries in the same vault the rest of
`internal/secrets` uses, under the same `Custody` backend `SelectCustody`
picked — on macOS the keychain via `/usr/bin/security`, on Linux the D-Bus
secret service, elsewhere the age-encrypted file vault. There is no
OAuth-specific store.

Keys are namespaced `oauth.<provider>.<account>.<role>.<generation>`, plus one
`oauth.<provider>.<account>.record` entry holding the `TokenRecord` JSON.

Two consequences follow, both deliberate:

- **Reading a token is an elevated verb**, because `Broker.Get` is. Minting a
  fresh bearer token from a stored refresh token is as disclosing as reading a
  secret, so it carries the same authorisation, and a broker wired without an
  `ElevationGate` refuses rather than proceeding.
- **A release binary is `CGO_ENABLED=0`**, so the backend that answers is
  always one of those three pure-Go ones. There is no cgo-gated path that is
  present in a development build and absent in a shipped one. A release binary
  puts a token in the OS keychain, the session secret service, or the encrypted
  file vault — never in a config file, an environment variable, or process
  memory beyond the single-use window.

### Replacement is one write

A refresh writes the new tokens under a **new generation's** key names, then
overwrites the record entry — which names those keys — in a single `Set`. That
`Set` is the commit point. A failure anywhere before it leaves the previous
record, and therefore the previous working credential, completely intact; the
caller gets `ErrCredentialInconsistent` and never a half-updated credential.
Superseded generations are removed after the commit, and a cleanup failure is
not reported as a credential failure, because the new credential is already
working.

## Tokens do not reach outputs

`TokenRecord` has no field a token could occupy. Inside `internal/secrets`,
every token, authorization code and PKCE verifier is carried in `oauthSecret`,
which implements `fmt.Formatter`, `fmt.Stringer`, `fmt.GoStringer` and
`json.Marshaler` so that *every* verb (`%s %v %q %x %#v`), every `Print`, and
every JSON encode yields `[redacted]`. Reaching the bytes requires calling
`Bytes()`, which reads as a decision at the call site.

No error constructor in the package accepts a token, a code, or a verifier. A
malformed callback is refused by naming *what* was wrong ("the redirect query
repeats the code parameter"), never by quoting the query — the query is where
the code lives. The authorization server's own error *code* is surfaced;
`error_description`, which is attacker-influenced free text, is not.

The redirect page cascade serves to the browser is fixed prose and never echoes
the query, so the browser tab, its history, and any screenshot of it carry
nothing.

## Implementing a driver

Drivers land in Wave 3 (J/S-19.T2 Anthropic, J/S-19.T4 Gemini). A driver:

1. Declares its `ProviderOAuthConfig` and calls `Validate()` at registration.
2. Obtains a broker from the composition root — it does not construct one, and
   it never reaches into `internal/secrets` itself.
3. Calls `AccessToken` per request and sends the bytes as a bearer credential.
4. On a 401, calls `Refresh` and retries once. On `ErrGrantRevoked`, tells the
   operator to re-authorize; the stored credential is already gone.

Do not cache an access token in the driver. The broker reads the vault on every
`AccessToken` call precisely so that a revoked grant cannot be papered over by
something a driver is still holding.

## Known gaps

- The recorded PKCE fixture
  (`internal/secrets/testdata/pkce-exchange-fixture.json`) was captured against
  a local RFC 7636-conformant server, not a vendor IdP. It pins cascade's
  request against the RFC's normative parameter set; it does not prove any
  particular vendor accepts it. That check belongs to the driver tickets, which
  have the real endpoints.
- `Start` files a new grant under the account label `default`. Multi-account
  providers name accounts at `Refresh` time; deriving an account identity from
  a provider's userinfo endpoint is a driver concern, not a broker one.
- At the JSON boundary the token briefly exists as a Go string, which is
  immutable and cannot be zeroed. The response buffer, which the broker does
  own, is zeroed. This is named rather than papered over with a zeroing call
  that would not do anything.

## The ModelProvider driver contract

`pkg/provider` (`types.go`, `model.go`, `events.go`, `compliance.go`) also
defines `ModelProvider` (P1-E10-W3-S19-T1): the vendor-neutral driver contract
every J/S-19 driver (anthropic, openai-compat, gemini, ollama) implements.
Exactly five verbs, and no sixth surface:

- `Chat(ctx, ChatRequest) (ChatResponse, error)` — one-shot completion.
- `Embed(ctx, ModelEmbedRequest) (ModelEmbedResponse, error)` — the
  model-side embedding call (distinct from the retrieval-side `Embedder`).
- `Count(ctx, CountRequest) (CountResponse, error)` — the provider's own
  token count for a piece of text.
- `Stream(ctx, ChatRequest, StreamSink) error` — a sequence of typed
  `StreamEvent`s (`delta`, `tool_call`, `usage`, terminating in exactly one
  `done` or `error`) delivered to the sink, in order.
- `Capabilities(ctx, lane string) (Capabilities, error)` — the descriptor the
  router matches requirements against; it describes a lane, it never routes.

A later ruling (R-21.28, AN/S-77.T5, W9) adds a sixth method,
`Discover(ctx) ([]LaneOffering, error)`; this interface is not closed against
that addition, it simply does not implement it yet.

ctx is always the crossing function's first parameter and is never stored in
a struct. No provider-specific type or credential shape crosses this
boundary — a driver obtains credentials through the `ProviderOAuthConfig` /
`OAuthBroker` flow described above, never through a `ModelProvider` field.

### Capabilities

`Capabilities` carries six R-14.88 tool-capability dimensions — `Search`,
`URLFetch`, `Vision`, `ToolUse`, `LongContext`, `StructuredOutput` — each a
tri-state `CapabilityState` (`unknown` the zero value, `supported`,
`unsupported`), resolvable per-lane since pool members' tiers differ per key.
`RequiredCapabilities` mirrors the same six dimensions on the request side;
`Capabilities.Satisfies(RequiredCapabilities)` reports whether every
dimension the caller set to true resolves to `supported`.

`Capabilities.CompliancePosture` (R-16.10) is every driver's vendor-terms
self-declaration: `AuthModes`, `InteractiveEntitlement`,
`ProgrammaticEntitlement`, `AutomationModes`, `Pacing`,
`MultiProfileEnabled`, and `CredentialSharing` — always
`CredentialSharingForbidden`. `NewCompliancePosture` is the only constructor
and has no parameter through which a driver could request anything else;
`Validate()` fails closed on any other value.

## Calling the model door

The frozen daemon client (`internal/client.Client`) gains no new method for
model execution. `pkg/provider/client.go` adds the typed door instead
(R-21.280):

- `RPCCaller` — the one-method seam
  (`Do(ctx, method string, params, out any) error`) the frozen client already
  satisfies.
- `Client`, built by `NewClient(c RPCCaller) *Client` — a thin value holding
  no socket, no transport, and no state beyond its `RPCCaller`, so
  `pkg/provider` stays free of `internal/` imports.
- `(*Client).ModelExecute(ctx, ModelRequest) (ModelResponse, error)` — exactly
  one `Do(ctx, "model.execute", req, &resp)` call, with any error mapped
  through the `pkg/cascade` taxonomy.
- `(*Client).JobCancel(ctx, JobID) error` — the idempotent
  `Do(ctx, "job.cancel", ...)` call; a repeat call for the same job is a
  no-op because `job.cancel` is idempotent server-side. Cancellation is never
  written to the SSE stream (`GET /events` is delivery only).

`plugins/pbd` and `internal/fleet` reach `model.execute` only through this
wrapper — neither package imports `internal/conductor`, which implements
`ModelExecutor` and owns the real execution plumbing.
