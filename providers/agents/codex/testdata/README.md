# codex driver test data

| Path | What it is |
|------|------------|
| `session.jsonl` | One App Server session: `{"dir": "c2s"\|"s2c", "t_ms": N, "frame": {...}}` per line. The replay App Server (the test binary launched as `<bin> app-server`, see `replay_test.go`) answers each client request with the block recorded after it, paced by `t_ms`. |
| `protocol/1/schema/` | Revision 1 of the protocol: the `ServerRequest` method enum (10 methods) and the five approval response types the driver writes. |
| `fuzz/FuzzCodexAppServerFrame/` | Seed corpus for the frame decoder. |
| `capture.sh` | Captures the schema and a session from an installed codex CLI. |

## Provenance

| Item | Tool | Version | Date | Status |
|------|------|---------|------|--------|
| `protocol/1/schema/` | written by hand to the shape of `codex app-server generate-json-schema` | codex 0.144.2 (target) | 2026-11-02 | **Not yet a capture.** No codex CLI was installed on the build host. Replace it with the full `generate-json-schema` output from `capture.sh` and diff it. |
| `session.jsonl` | composed to the App Server wire shape | codex 0.144.2 (target) | 2026-11-02 | **Not yet a capture.** The same applies: re-record it with `capture.sh` under a throwaway `CODEX_HOME` and keep the line format. |

Until both files come from `capture.sh`, the conformance replay proves the
driver against this fixture, not against a real codex session. The pinned
version (`PinnedCodexVersion`) and `ProtocolRevision` change only together
with a new `protocol/<n>/schema` directory.

The replay rewrites `codexHome` in the initialize result to the test's own
`CODEX_HOME`. A recorded path never leaks into a test, and the driver's
custody check (codexHome equals the profile auth dir) runs against the real value.
