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
