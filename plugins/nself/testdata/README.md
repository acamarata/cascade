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
- `nself_add_cascade` returns one typed refusal naming both missing
  prerequisites (the CLI verb, and the host's absent config diff-apply
  seam). There is no handshake parser, no response fixture and no handshake
  fuzz corpus in this package: the deleted draft had all three, built from
  the contract's prose rather than from a captured response, which is the
  invented-dialect failure Art.2 exists to prevent.
- A project whose services are unhealthy makes `nself status` exit 2, so the
  SUBPROCESS leg reports "not detected" there. The marker scan is the
  primary signal and is unaffected; this is a disclosed limit of an exit-code
  probe, not a hidden one.

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
