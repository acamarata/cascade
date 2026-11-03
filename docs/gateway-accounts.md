# Gateway accounts

The gateway account contract is `contract:gateway-account`. Configure accounts
under `[gateways.<name>]`. Names match `^[a-z0-9][a-z0-9-]{0,31}$`.
Unknown keys and malformed values are refused.

| Key | Meaning |
| --- | --- |
| `base_url` | Required HTTPS base URL. HTTP is allowed only for literal `127.0.0.1`, `[::1]`, or `localhost`. Userinfo, queries and fragments are refused. |
| `header` | `bearer` (default) or `x-api-key`. |
| `key_env` | Environment variable containing the token, read on every call. |
| `credential_helper` | Absolute executable path, mutually exclusive with `key_env`. |
| `helper_sha256` | Required with a helper: 64 lowercase hexadecimal digits pinning its bytes. |
| `models` | Optional list of synthetic or owner-configured model IDs. Absent means discovery via GET `<base>/v1/models`; an explicit empty list is a declared empty catalog. |

Exactly one credential source is required. An empty environment variable fails
with `unavailable`; no vault, keychain or secret service is consulted.
All accounts are terms-sensitive. The stable account ID lowercases scheme and
host, drops the default port and removes trailing slashes from the path.

An optional `[gateways.<name>.usage]` table declares `source = "endpoint"` or
`"headers"` and `window = "5h"`, `"day"`, `"week"` or `"month"`.
Endpoint sources require a `path` beginning with one slash and accept RFC 6901
`used_pointer`, `limit_pointer`, `utilization_pointer`, and `resets_at_pointer`.
Header sources accept `utilization_header` and `resets_header`.
Fields for the other source kind are refused. This record declares the reader;
it does not itself fetch usage.

Adding an account or changing any configured field is a guarded loosening.
Removing an account tightens the configuration. Environment overrides such as
`CASCADE_GATEWAYS__ALPHA__BASE_URL` enter the same comparison. A divergent or
unaccepted configuration runs without those accounts until the exact
configuration is accepted through `config.accept_loosening`.

See [credential helpers](security-posture/credential-helpers.md) before enabling
an executable credential source.

## Add

`cascade provider add <name> --gateway <account> [--pool P] [--no-verify]`
registers an account from the on-disk effective configuration. It pins the
anthropic driver and creates an `api_credit` lane. `--key`, `--key-env`,
`--oauth`, `--base-url` and `--kind` cannot accompany `--gateway`.
The provider stores only `auth_type = gateway` and `auth_ref = gateway:<account>`.
Tokens are resolved at call time and never written to a vault or database.

## Catalog

A declared `models` list skips discovery. Otherwise registration calls
GET `<base>/v1/models` with the account's header style. A missing catalog
refuses registration: declare `models` or use a gateway with `/v1/models`.
The first model is used for the one-token micro-verify. `--no-verify` records
an unknown lane state. Catalog source and observation time are recorded.

## Reauth

`cascade provider reauth <name>` resolves the configured source again and
re-verifies the stored first model. Credential overrides and `--no-verify`
are refused for gateway records. Success marks the lane available; failure
leaves the record and lane unchanged.

## Posture

Gateway providers are operator-configured and terms-sensitive. Doctor names
each registered gateway provider with that posture. Static bearer-key auth
is programmatic and has no interactive entitlement or OAuth refresh path.

## Dispatch

The daemon resolves a gateway provider against the gateway accounts in its
admitted configuration, never against the provider record alone. The daemon
reads the admitted accounts once at start, so an account accepted later
applies at the next start.

- The account named by `gateway:<account>` must be admitted. Otherwise the call
  is refused with `permission_denied` before any request: "gateway account
  <account> is not admitted; run `cascade config accept` after reviewing
  [gateways.<account>]".
- The record's base URL must equal the account's base URL exactly. A record
  that differs, for example a tampered `providers.db`, is refused with
  `conflict` and nothing is sent. Re-register it with
  `cascade provider add <name> --gateway <account>`.
- The request goes to the account's base URL with the account's header style:
  `authorization: Bearer <token>` for `bearer`, `x-api-key` for `x-api-key`.
- The token comes from the account's `key_env` or credential helper at call
  time, from the daemon's own environment, home and user. A `gateway:` reference
  is never read from the vault. Every other provider keeps the standing-grant
  vault reader.
- A helper that fails makes the call fail with `unavailable`, naming the helper
  path. No request is sent and the lane row is unchanged.
- Each call records its outcome on the lane like any other provider. Retry,
  backoff and health writes are not part of dispatch.

## Quota

The provider usage store (`provider-usage.db`, table `provider_quota_window`,
schema version 7) holds the last quota-window reading per account and window.
Fleet capacity and the widget read it through one seam; no second store exists.

Sources:

| `source` | Status | How it is read |
|---|---|---|
| `endpoint` | implemented | `GET <base_url><path>` every 60 s per admitted account, after acquiring the `provider-usage` egress capability; the body is decoded over the four RFC 6901 pointers |
| `headers` | refused | parses, but every use returns KindUnsupported; no capture of this kind exists yet |

A missing pointer leaves that figure unknown (`-1`). Utilization is the
reported value when present, else `100 * used / limit` when `limit > 0`, else
unknown. Known values are clamped to 0..100.

Stale semantics: a failed read keeps the last values and stores only a
content-free class (`egress-refused`, `credential`, `unavailable`,
`status-<code>`, `decode`, `store`). A window reads stale when it has a last
error or its last success is older than 120 s (twice the poll interval).

Mapping:

| Window | fleet.capacity | widget row |
|---|---|---|
| `5h` | `quota[]` and the bucket's `five_hour.utilization_pct`; `resets_in` from the window reset when later than the lane estimate | `five_hour.utilization_pct` |
| `week` | `quota[]` and the bucket's `seven_day.utilization_pct` | `seven_day.utilization_pct` |
| `day`, `month` | `quota[]` only | not shown |

A stale or failed window never fills a utilization: the widget shows null and
`quota[].stale` is true, with the last values and `observed_at`. Nothing maps
an unknown or stale window to 0.
