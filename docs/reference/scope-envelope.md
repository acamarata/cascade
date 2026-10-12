# Scope envelope

The one scope envelope for delegated work (ADR-0007). Package home:
[`pkg/envelope`](../../pkg/envelope). An envelope states what a single
dispatch may touch: filesystem roots, tools, commands, egress classes,
credential grant references, a resource budget with typed extensions, a
fencing token, a [sensitivity tier](sensitivity.md), the caller principal
and the issuer. The controller signs it with its installation key. A
receiver verifies it against the pinned controller key. A delegating holder
derives a smaller one with `Narrow`.

There is exactly one envelope type. Dispatch frames embed `envelope.Signed`;
they never define a second envelope or delegation struct.

## Fields

| Field | Wire name | Rule under `Narrow` |
|---|---|---|
| `Version` | `v` | Equal to the parent's (always `1`). |
| `ID` | `id` | Free. |
| `ParentDigest` | `parent` | Must equal `Digest(parent)`. |
| `Issuer` | `iss` | Equal to the parent's. Form: `ed25519:` + hex(sha256(controller public key)). |
| `Principal` | `principal` | May differ. A caller-principal ID, never a token. |
| `Roots` | `roots` | Each child root equals or lies under a parent root at a path-segment boundary (`/a/bc` is not under `/a/b`). |
| `Tools` | `tools` | Subset of the parent's, exact strings. `*` is never valid (refused as non-normalised). |
| `Commands` | `cmds` | Subset, as for tools. |
| `Egress` | `egress` | Subset, as for tools. |
| `Grants` | `grants` | Each (`id`, `verb`) appears in the parent and expires no later. |
| `Budget` | `budget` | Every component (`wall`, `tok`, `cost`, `procs`) at most the parent's. Zero grants none. |
| `Ext` | `ext` | Same kinds as the parent, each registered, each passing its narrower. Dropping a parent extension widens and is refused. |
| `Fence` | `fence` | Equal to the parent's. |
| `Sensitivity` | `sens` | Equal or more restrictive. |
| `IssuedAt` | `iat` | No earlier than the parent's, so the child's validity window lies inside the parent's. |
| `ExpiresAt` | `exp` | No later than the parent's. |

A refusal is a `*NarrowError` whose `Field` is the wire name above and whose
`Unwrap` is `KindPermissionDenied`.

## Canonical form

`Canonical(e)` is the domain separator `cascade.scope-envelope.v1` plus a
newline, followed by the JSON of the envelope with every time in UTC.
`Digest(e)` is hex(sha256(Canonical(e))). An envelope has exactly one
encoding, so non-normalised input is refused with `KindInvalidInput`, never
repaired:

- every list sorted and free of duplicates and empty entries; grants sorted
  by (`id`, `verb`); one extension per kind, sorted by kind;
- roots absolute, clean and slash-separated (`/a/b/../../etc`, `a/b` and
  `/a/b/` are refused);
- no `*` in tools, commands or egress classes;
- every string valid UTF-8, and extension data that is valid JSON obeying
  the same key and depth rules `Decode` enforces (so `Issue` never signs
  an envelope its peer refuses, and no two envelopes share signed bytes);
- a declared sensitivity tier, no negative budget component, times inside
  years 0000-9999 in UTC;
- at most 64 roots, 256 tools, commands or egress classes, and 32 grants or
  extensions;
- a canonical body small enough that the signed wire form
  (`{"env":…,"sig":"<88 base64 bytes>"}`) fits in 64 KiB, so every
  envelope `Issue` signs can be decoded by its peer.

## Signing and verification

`Issue(ctx, signer, e)` fills an empty `Issuer` from the signer's key,
refuses a different one, and signs `Canonical(e)`. The signer is the
controller installation key custody; this package never creates, stores or
pins a key.

`Verify(s, pinned, now)` checks, in order:

1. the pinned key is exactly 32 bytes (a nil, empty or short key is
   `KindIntegrity` and never panics);
2. a 64-byte signature is present;
3. `Issuer` names the pinned key;
4. the envelope is canonical and the signature verifies over
   `Canonical(env)`;
5. `IssuedAt <= now < ExpiresAt` (outside the window is
   `KindPermissionDenied`).

Steps 1 to 4 fail with `KindIntegrity`. A time-zone shift that keeps the
same instant keeps the signature valid, because the canonical form is UTC.

## Extensions

`RegisterExtension(kind, narrower)` registers, at init time, the function
that decides whether a child's extension data stays within the parent's.
A duplicate, empty or nil registration panics. An unregistered kind always
refuses under `Narrow`.

## Decoding

`Decode(b)` parses untrusted wire bytes into a `Signed`. It does not check the
signature; call `Verify` next. It refuses with `KindInvalidInput`:

- input over 64 KiB, or input that is not valid UTF-8;
- input whose re-encoding could pass 64 KiB (each raw `<`, `>`, `&`,
  U+2028 or U+2029 grows when Go's JSON encoder escapes it), so every
  accepted input re-encodes to an input `Decode` also accepts;
- unknown or missing fields (only `parent` and `ext` may be absent), a
  value of the wrong JSON type (times, tier names and IDs are strings,
  counts are numbers, nested structs are objects; `null` is refused), and
  a signature that is not 64 bytes;
- times not written as `YYYY-MM-DDTHH:MM:SS`, an optional 1 to 9 digit
  fraction, then `Z` or `±HH:MM`; a tier that is not one of the four names
  (an empty tier is refused); and a number longer than 20 characters;
- object keys holding `<`, `>`, `&`, U+2028 or U+2029, which the encoder
  would re-escape;
- duplicate keys, escaped keys, and keys that only case-fold to another key
  (`SIG`, or the Kelvin sign for `k`), in every object including extension
  data, because Go's JSON decoder would otherwise accept them silently;
- extension data objects with more than 64 keys, nesting deeper than 32,
  over-long lists, and trailing data;
- any non-normalised envelope.

The decoded envelope is returned in canonical shape: times in UTC and
extension data compacted.

Several wire encodings (whitespace, string escapes, time offsets) carry
one signed envelope. Key replay protection and deduplication on
`Digest(env)`, never on wire bytes. Error messages quote at most 48 bytes
of input.

These checks run in an allocation-free pass before the JSON decoder
allocates anything. One call allocates less than 4 × 64 KiB. A fuzz test
(`FuzzDecodeSigned`) proves the decoder never panics, holds that bound, and
re-encodes every accepted input to identical canonical bytes.
