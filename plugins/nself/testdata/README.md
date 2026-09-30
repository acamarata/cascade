# cascade-nself testdata provenance

Art.2 (12-QUALITY-CONSTITUTION.md): a contract this plugin does not own is
tested against evidence captured from the real counterpart, never against a
dialect this package invented for itself.

This plugin has exactly one external counterpart, the `nself` CLI. What was
probed, and what the probe returned, is recorded here in full — and the
things the ticket's contract named that the counterpart does NOT have are
recorded as absences rather than modelled as fixtures.

## The counterpart, probed read-only (build machine, 2026-09-21)

`nself` v1.3.5 (`nself --version` → `1.3.5`).

| Contract names | Reality in v1.3.5 |
|---|---|
| `nself project status --json` (DETECTION) | no `project` verb at all: `unknown command "project"` |
| `nself add cascade` (HANDSHAKE) | `nself add <name>` is the short form of `nself plugin install <name>`: a registry plugin installer. Emits no JSON, carries no server-profile fields |
| — | `nself status --json` EXISTS: `nself status --help` documents `-j, --json`, exit 0 healthy / 1 error / 2 unhealthy |

`nself status --json` run read-only in `/private/tmp` (not a project) prints
`Error: no nself project found in current directory or parents. Run 'nself
init' to create a project` and exits **1**. That is the outcome this
plugin's probe folds to "not detected", with the exit code kept for the
doctor leg.

### Consequences, stated rather than papered over

- The probe targets `nself status --json` (real) instead of `nself project
  status --json` (absent).
- A project whose services are unhealthy makes `nself status` exit 2, so the
  SUBPROCESS leg reports "not detected" there. The marker scan is the
  primary signal and is unaffected; this is a disclosed limit of an exit-code
  probe, not a hidden one.
- SUPERSEDED BY P1-E25-W5-S103-T1 (below): the earlier "no handshake
  fixture" position above (S-52.T2) is resolved by the transcript this
  section records — `nself version --json` and `nself config get <KEY>`
  are real verbs, captured and replayed rather than invented.

## The handshake transcript, `nself` v1.3.5 (P1-E25-W5-S103-T1, 2026-09-23)

Captured from the SAME installed binary (`nself --version` → `1.3.5`,
resolved via `PATH`, sha256
`277e4a2344298b6a45e027fdb602b78a3b1d6d769ae2758593e4e34b6e9b4c48`) in a
throwaway project created with:

```
mkdir -p /tmp/cascade-nself-fixture && cd /tmp/cascade-nself-fixture
nself init --non-interactive --name cascadefixture
```

Then, from that project directory:

```
nself version --json > transcripts/nself-1.3.5/version.json
nself config get POSTGRES_HOST        > transcripts/nself-1.3.5/config-get-POSTGRES_HOST.txt
nself config get POSTGRES_PORT        > transcripts/nself-1.3.5/config-get-POSTGRES_PORT.txt
nself config get POSTGRES_DB          > transcripts/nself-1.3.5/config-get-POSTGRES_DB.txt
nself config get POSTGRES_EXTENSIONS  > transcripts/nself-1.3.5/config-get-POSTGRES_EXTENSIONS.txt
nself config get REDIS_ENABLED        > transcripts/nself-1.3.5/config-get-REDIS_ENABLED.txt
nself config get REDIS_PORT           > transcripts/nself-1.3.5/config-get-REDIS_PORT.txt
nself config get MINIO_ENABLED        > transcripts/nself-1.3.5/config-get-MINIO_ENABLED.txt
nself config get MINIO_PORT           > transcripts/nself-1.3.5/config-get-MINIO_PORT.txt
nself config get S3_BUCKET            > transcripts/nself-1.3.5/config-get-S3_BUCKET.txt
```

RECAPTURED 2026-09-23 (S-103.T1 rework) from the same binary (same sha256)
in a fresh `nself init --non-interactive --name cascadefixture` project in a
scratch directory, this time recording each command's real stdout byte for
byte and its exit status separately:

```
for k in POSTGRES_HOST POSTGRES_PORT POSTGRES_DB POSTGRES_EXTENSIONS \
         REDIS_ENABLED REDIS_PORT MINIO_ENABLED MINIO_PORT S3_BUCKET; do
  nself config get $k > transcripts/nself-1.3.5/config-get-$k.txt 2>/dev/null
  echo "$k $?" >> transcripts/nself-1.3.5/exit-codes.txt
done
```

The first capture's four unset-key files held a single `\n`; the real
stdout of a failing `nself config get` is 0 bytes, which is what they hold
now. `exit-codes.txt` (`<KEY> <rc>` per line) is the rc column below in
machine-readable form, and `TestHandshakeRealTranscript` replays a non-zero
rc as the probe failure the real runner returns, instead of inferring it
from empty output. `version.json` and the five set-key files were
byte-identical on recapture.

No `--reveal` was ever passed; no credential key (`POSTGRES_PASSWORD`,
`*_SECRET`, `*_KEY`, `*_TOKEN`) was ever requested — the project's real
`.env` carries `POSTGRES_PASSWORD` alongside these, and it is not among the
captured files.

| Key | Exit | Captured value | Note |
|---|---|---|---|
| `POSTGRES_HOST` | 0 | `postgres` | |
| `POSTGRES_PORT` | 0 | `5432` | |
| `POSTGRES_DB` | 0 | `cascadefixture` | |
| `POSTGRES_EXTENSIONS` | 1 | (empty) | unset on a fresh `nself init`; stderr `Error: key not found: POSTGRES_EXTENSIONS` |
| `REDIS_ENABLED` | 0 | `false` | default-disabled on init |
| `REDIS_PORT` | 1 | (empty) | unset while Redis is disabled; stderr `Error: key not found: REDIS_PORT` |
| `MINIO_ENABLED` | 0 | `false` | default-disabled on init |
| `MINIO_PORT` | 1 | (empty) | unset while MinIO is disabled; stderr `Error: key not found: MINIO_PORT` |
| `S3_BUCKET` | 1 | (empty) | unset while MinIO is disabled; stderr `Error: key not found: S3_BUCKET` |

`version.json` (identical to `nself version --json` run outside any
project — the command is not project-scoped):

```json
{
  "buildDate": "2026-08-30T21:57:59Z",
  "commit": "ff0ba27b",
  "goVersion": "go1.26.6",
  "platform": "darwin/arm64",
  "version": "1.3.5"
}
```

`TestHandshakeRealTranscript` (handshake_transcript_test.go) replays these
exact bytes through a recording `subprocessRunner` and asserts the
handshake's decoded values and proposed diff match this table.
`TestHandshakeLiveNself` runs the actual installed binary when `nself` is
on PATH (it is, on this build machine) and names the binary rather than
skip silently otherwise. It creates its own `nself init --non-interactive
--name cascadefixture` project under `t.TempDir()` and runs PROPOSE mode
only, so no config is ever applied. `FuzzNselfVersionJSON`'s seed is `version.json`
above.

### HONEST GAP

The fixture project has Redis and MinIO disabled (nself's own
non-interactive default), so the captured transcript exercises the
"disabled/unset" branch of `REDIS_PORT`/`MINIO_PORT`/`S3_BUCKET`/
`POSTGRES_EXTENSIONS` for real, but not the "present" branch (e.g. a
project with `POSTGRES_EXTENSIONS=vector`, `REDIS_ENABLED=true`) against
the real binary — that branch is exercised only by hand-built fixtures in
`TestHandshakeProposesDescriptors`/`TestHandshakePgvectorDSNOptional`,
not replayed from a second real capture. A project with pgvector and Redis
enabled was not created for this capture pass.

## The project marker, from real projects (read-only listings, 2026-09-21)

Detection requires a `.nself` DIRECTORY holding at least one file nself
itself writes. The file set in `detect.go`'s `projectFiles` comes from these
real directories on the build machine:

| Directory | Contents observed |
|---|---|
| four independent `*/backend/.nself` project dirs | `.first-run-complete`, `build-state`, `build-version`, `compose-files.txt`, `dist`, (some also `cache`, `compose.env`, `config`, `health`, `servers`, `sync`) |
| one non-backend project's `.nself` | `nself.yml` |
| one parent directory's `.nself` | `pipelines` only — correctly NOT a project |
| `$HOME/.nself` (nself's global state dir) | `bin`, `cache`, `license`, `plugins`, `runtime`, `install.sh`, … — none of the project files, and refused by path in any case |

`nself.toml` appears in **no** real project: the draft this replaced invented
it, and every directory under the home directory false-positived through
`$HOME/.nself` as a result. Both are now pinned red-first by
`detect_test.go`.

## Fuzz corpora

Package-local per R-21.266 (`<pkg>/testdata/fuzz/<FuzzName>/`):

- `fuzz/FuzzNselfToolInput/seed001` — the tool body decoder. Oracle: never
  panics, and any reported `root_dir` agrees with an independent
  `encoding/json` decode into a generic map.
- `fuzz/FuzzNselfProjectInfoPayload/seed001` — the response scrub. Oracle:
  the encoded payload is valid JSON and no URL-shaped run in it carries
  userinfo (checked by hand-extracted authority, with `net/url` as a second
  opinion). Neither oracle calls the code under test, which the deleted
  handshake fuzzer's did.

- `fuzz/FuzzNselfVersionJSON/seed001` — the `nself version --json`
  decoder, seeded with the real `version.json`. Oracle: never panics, and a
  decoded version equals the string under the EXACT key `version` of an
  independent generic-map decode.

`fuzz/FuzzNselfVersionJSON/3a944ed75537fa99` (`{"Version":"0"}`, found by
the S-103.T1 review's 5 s fuzz) and `seed002` (`{"VERSION":"9.9.9"}`) are
REGRESSION seeds for the CODE: `encoding/json` binds struct fields
case-insensitively, so the first decoder read `VERSION` as the version and
let 9.9.9 clear the 1.3.5 floor. The decoder now looks up the exact key.

`fuzz/FuzzNselfToolInput/seed002` is a REGRESSION seed for the ORACLE, not
for the code: the confirming review's live fuzz minimised `{"Root_dir":"0"}`
in 0.35s, because `encoding/json` binds struct fields case-insensitively when
no exact key match is present while the oracle's generic-map decode asked for
`root_dir` exactly. The behaviour is Go's documented default and harmless
here; the oracle now looks the key up case-insensitively, and this input stays
in the corpus so the over-stated oracle cannot return.

`fuzz/FuzzNselfProjectInfoPayload/seed002` and `seed003` carry the four scrub
survivors the same review found — `aws_access_key_id=…`,
`AWS_SECRET_ACCESS_KEY=…` (neither could match a `\b` placed after an
underscore), `Bearer <token>`, and a bare 40-hex token (the one shape that
survived the egress firewall as well). Every value in them is a published
documentation placeholder, not a credential.

`fuzz/FuzzNselfProjectInfoPayload/f0d146a1bbdf5b4b` is a REGRESSION seed the
live fuzzer found on its first run of this ticket's fix: a value whose
"scheme" is a character `encoding/json` escapes (`&://000@000`) slipped past
a scheme-anchored redaction pattern and then reappeared as `u0026://000@000`
in the encoded bytes. The pattern now matches userinfo in an authority
regardless of what precedes `://`, and this input stays in the corpus so the
regression cannot return.

`fuzz/FuzzNselfProjectInfoPayload/f2a67dc0f6acd5b2` is the SECOND regression
seed of the same family, found by this ticket's fix-round live fuzz in 3s:
`A://<CR>@`. A raw carriage return is whitespace, so an authority class
written as `[^/?#\s]` stopped at it and skipped the redaction — while
`encoding/json` re-emitted it as the two printable characters `\r`, putting
userinfo back into the encoded bytes. The class now excludes only a literal
space, which is the only raw byte that is still a separator after encoding.

The `not-a-real-secret` password in the seed is a literal placeholder, not a
credential.
