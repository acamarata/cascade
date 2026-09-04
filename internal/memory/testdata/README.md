# internal/memory/testdata

## v1-goldens

Four checked-in fixtures, one per ratified `MemoryKind`, that pin format
version 1 of the on-disk memory record. `TestGolden_Decode` parses each
one and asserts the exact record it decodes to; `TestGolden_Encode`
asserts that encoding that record reproduces the file byte for byte.

These files are the format contract, not a cache of a test run. Once
records exist on a user's disk, a change to the layout, the key set, the
key order, the quoting, or the timestamp encoding either migrates those
records or corrupts them. A change here that is not also a deliberate
format change is a bug in the change.

### Provenance (Article 2)

There is no external producer of this format to harvest a fixture from,
so the honest account of where these files came from is this:

- **The frontmatter shape** was transcribed from live memory files
  produced by the v1 harness, whose protocol R-14.20 ratifies: a `---`
  fenced header of `key: value` lines carrying `name`, `description` and a
  record type drawn from the same four-kind taxonomy, followed by a
  markdown body. The v2 format is that shape made strict (every value a
  quoted literal, an explicit format version, a fixed key order, the
  provenance block flattened into the header rather than nested).
- **The files themselves were hand-authored** in an editor, before the
  encoder could produce them, and are never regenerated. There is no
  update mode for this fixture set and no `-update` flag: a golden that
  its own producer can rewrite proves only that the producer agrees with
  itself, which is exactly the failure these fixtures exist to catch.
- **The `content_hash` values** were computed with the BLAKE3 reference
  algorithm over each body. That algorithm is independently pinned in
  `types_test.go` by the published BLAKE3 reference test vectors for
  inputs of length 0, 1 and 1023 bytes, so the digest column is anchored
  to the specification rather than to this package's own helper.

### What each fixture is for

| File | Covers |
|---|---|
| `entry_user.md` | The plain case: ASCII throughout, no optional fields set. |
| `entry_feedback.md` | A description containing a colon, escaped quotes and a `#`; a body containing a `---` line, which must not be mistaken for the closing fence; all optional fields set; sub-second `created_at`; confidence at the upper bound. |
| `entry_project.md` | A body in mixed scripts (Arabic, CJK, Greek, a combining sequence); a fractional-second timestamp that is not nanosecond-precise. |
| `entry_reference.md` | The empty cases: empty description, empty body, confidence at the lower bound, and a TTL set. |

## Fuzz corpus

`FuzzFrontmatterParse`'s seed corpus lives at
`internal/testdata/fuzz/FuzzFrontmatterParse/`, not here, per the module
convention that every fuzz corpus shares that one home. See that
directory's own README for seed-by-seed provenance.
