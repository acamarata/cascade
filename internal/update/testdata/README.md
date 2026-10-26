# internal/update test fixtures

| File | Provenance |
|---|---|
| `releases-latest-tag.txt` | Recorded 2026-10-19 with `curl -sI https://github.com/goreleaser/goreleaser/releases/latest` (status line and `location` header only). The shape a project with a published release answers: a 302 to `/releases/tag/<tag>`. |
| `releases-latest-none.txt` | Recorded 2026-10-19 with `curl -sI https://github.com/acamarata/cascade/releases/latest` while the repository had no published release: a 302 to `/releases`, which carries no tag and must refuse. |
| `metadata.json` | The `dist/metadata.json` shape goreleaser's metadata pipe writes (`project_name`, `tag`, `previous_tag`, `version`, `commit`, `date`, `runtime`). Hand-written to that shape with a snapshot version, not captured from a run; the snapshot e2e leg reads the real file. Only `tag` is read. |
| `fuzz/FuzzChecksumsParse/` | Seed corpus: a goreleaser-shaped checksums file (archive, SBOM and installer lines), a path-carrying name, a duplicate name. |
| `fuzz/FuzzInstallReceiptParse/` | Seed corpus: the receipt install.sh writes (schema 1, three files), an unsafe name, a schema 2 receipt. |

Signed dists, archives and keys are generated per test under `t.TempDir()` by `helpers_test.go`
(ephemeral ed25519 keys emitting minisign-format signatures that `internal/minisign` verifies).
No fixture holds a real key, token or signed query.
