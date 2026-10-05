# Elevation

Elevated verbs require custody that proves local presence. The selector ranks sources by their registered custody tier, independently of the keystore's storage label or registration order.

| Custody | Elevation |
| --- | --- |
| platform | Allowed through the protected platform source |
| presence | Allowed through a registered presence source |
| file | Refused in release builds; signing exists only with the `devkeys` build tag |
| none | Refused |

A file key that reports an OS keychain storage label remains file custody. An existing file key never outranks an available platform or presence source. Enrollment never falls back to file storage after a protected source fails.

## Release availability

The cgo-free macOS and Linux release builds refuse elevated operations until a protected helper or presence source is available. Linux PAM and keyring storage do not qualify as platform custody: the stored key can be read without authentication. Windows remains tier 2 and refuses elevation.

Custody refusals use `KindUnsupported`, JSON-RPC code `-32012`, and data containing `reason: ELEVATION_CUSTODY_TIER` and the selected tier. They do not issue a challenge or run an elevated handler. The read-only custody report exposes the selected tier, source, enrollment and binding status; doctor integration is separate.

## Enrollment and stale file keys

The enrolled public key must equal the selected custody key. A swapped trust record refuses. On POSIX, trust records must have mode `0600` and belong to the effective user.

When a protected source becomes available, an old enrollment bound to `elevation.key` requires explicit replacement:

```text
cascade elevate-helper --enroll --replace-file-tier
```

Replacement is allowed only when the old trust record matches the file key. It stores the new trust record before removing the stale key. A record for an unrelated key remains untouched.

## Signing

Authentication and signing share one operation. Attestations bind the request, action and nonce and expire after five minutes. Nonces are single use. The `devkeys` tag permits file signing for development; it does not prove local presence and is unsuitable for release artifacts.
