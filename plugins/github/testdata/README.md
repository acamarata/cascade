# cascade-github fixture provenance

Art.2 (12-QUALITY-CONSTITUTION.md): the GitHub REST API is an external
contract this plugin does not control, so its decoders are tested against
responses captured FROM the real API — never against a dialect this package
invented for itself.

## Capture

| | |
|---|---|
| Tool | GitHub CLI (`gh`) 2.78.0 (2025-08-21) |
| Date captured | 2026-09-14 |
| API | `https://api.github.com`, REST, unpinned default version |
| Method | `gh api <path> > <fixture>` — stdout only, no flags that reshape the response |

| Fixture | Request |
|---|---|
| `repos.get.json` | `repos/acamarata/cascade` |
| `repos.list.json` | `users/acamarata/repos?per_page=2&sort=updated` |
| `issues.list.json` | `repos/cli/cli/issues?per_page=2` |
| `prs.list.json` | `repos/cli/cli/pulls?per_page=2` |
| `error.404.json` | `repos/acamarata/this-repo-does-not-exist-xyz` (a genuine 404 body) |

Issues and pull requests come from a different public repository than the
others for a plain reason: `acamarata/cascade` has none, and an empty array
would exercise none of the fields the decoders read.

## What was changed after capture, and what was not

These fixtures ship in a PUBLIC repository, so third-party personal data was
removed. Nothing else was touched.

Redacted:

- every `login` other than the project's own `acamarata` (which is already
  the module path) becomes `contributor-N`, applied consistently so the same
  person reads the same way across every fixture and every URL that embeds
  the login;
- `name`, `email`, `gravatar_id` and `user_view_type` **only inside an object
  that carries a `login`** — that is what makes them personal. A repository's
  own `name` is not, and survives intact (`repos.get.json` still reads
  `"name": "cascade"`), because the decoders read it;
- `body`, `title` and `description`, which are user-authored free text and
  can contain anything.

NOT changed: no key was added, removed or renamed; no value changed type; no
object was flattened, reordered or pruned; no response was truncated. Every
redaction replaces a string with a string. The structure under test — which
is the whole point of capturing rather than authoring — is exactly what the
API returned. `repos.get.json` still carries all 97 top-level keys.

Re-indented to two spaces for readability. That is a formatting change to
whitespace between tokens and is the only such change.

## Renewing these fixtures

Re-run the captures above and re-apply the same redaction rules. Do NOT
regenerate a fixture from this plugin's own output: a fixture that agrees
with the decoder by construction tests nothing. If the API's shape has
changed, that difference IS the finding — record it rather than smoothing it
away.

## Fuzz corpus

`../tools/testdata/fuzz/FuzzDecodeGitHubResponse/seed001` seeds the decoder
fuzz target from the shape of these captures (R-21.266: package-local corpus
under `<pkg>/testdata/fuzz/<FuzzName>/`).
