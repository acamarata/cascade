# internal/secrets test fixtures

Provenance for every fixture in this directory, per the repo's real-counterpart
fixture rule: a golden must be produced by the tool whose behaviour it pins,
never by the code under test.

## file-vault-golden.age

| | |
|---|---|
| What it is | An age v1 encrypted file, scrypt (passphrase) recipient. |
| Produced by | `age` v1.3.2, the reference implementation, invoked as `age -p -o file-vault-golden.age <plaintext>`. |
| Produced on | 2026-09-04. |
| Passphrase | `cascade-file-vault-golden-passphrase` (a fixture passphrase, not a credential; it protects nothing). |
| Plaintext | `{"DEMO_TOKEN":"c2FtcGxlLXZhbHVl"}` - the shape the encrypted file vault stores, a JSON object of name to base64 value. The value is the literal string `sample-value`. |
| scrypt work factor | 18, the `age` CLI default. |

**Why it exists.** The encrypted file vault writes real age v1 files rather than a
look-alike format, so an operator can open a cascade vault with the stock `age`
tool. `TestAgeDecryptRealAgeFixture` decrypts this file with the package's own
reader. Because the file came from the reference implementation and not from
this package, a passing test proves interoperability; a fixture this package had
written itself would only prove that it agrees with itself.

**Regenerating it.** Only regenerate if the age format itself changes. Run the
real `age` tool (not this package) with the passphrase above, record the tool
version and date in the table, and confirm `TestAgeDecryptRealAgeFixture` still
passes without any change to the reader.

## detector/ — the secret-detector corpus

Provenance for the detector fixtures (P1-E08-W2-S15-T3), all hand-authored on
2026-09-04. **None of these is a real credential.** Each was written to carry the
STRUCTURE the detector keys on — a vendor prefix, a JWT triplet, PEM armour, a
URL authority, a base64 blob that decodes to JSON — with randomly typed body
characters, so a fixture proves the shape is matched without any of them ever
having authenticated to anything.

| File | Class | How it was derived |
|---|---|---|
| `api-key.txt` | api-key | An `OPENAI_API_KEY=` assignment carrying the published `sk-` prefix followed by 46 typed characters. |
| `jwt.txt` | jwt | The canonical RFC 7519 example header/payload (`{"alg":"HS256","typ":"JWT"}` / a `sub`+`name` claim set), base64url-encoded, with a typed signature segment. |
| `bearer.txt` | bearer | An `Authorization: Bearer` header whose token is the base64 of the ASCII string `FOR-TESTING-ONLY-not-a-real-token`. |
| `pem.txt` | pem-private-key | Real OpenSSH PEM armour lines around one truncated body line from an `ssh-keygen` header block; the key material is absent, so the block decodes to nothing. |
| `conn-str.txt` | connection-string | A `postgres://user:pass@host:5432/db` DSN with a typed password. |
| `base64-json.txt` | base64-json | `{"api_key":"XXYYZZ-sample-value-0001"}` run through `base64 -w0` with padding stripped. |
| `plain-prose.txt` | none (must be clean) | Three sentences of ordinary standup prose, deliberately free of any 16-character alphanumeric run. |

The near-miss files are the measured-precision corpus: content that looks
credential-shaped to a naive detector and is not. Each must produce **zero**
quarantine entries end to end.

| File | What it is | Why it is a near miss |
|---|---|---|
| `near-miss-uuid.txt` | An RFC 4122 v4 UUID in a `trace_id` JSON field. | Above any usable entropy floor; a trace id, not a secret. |
| `near-miss-gitsha.txt` | A 40-character lowercase hex git object id in a commit sentence. | Same entropy profile as a hex API key. |
| `near-miss-b64-image.txt` | A `data:image/png;base64,` URI holding a real 1x1 PNG. | A long, high-entropy base64 run that decodes to image bytes, not JSON. |
| `near-miss-high-entropy.txt` | A typed 32-character mixed-case build fingerprint. | High entropy with no structural marker and no credential-named field nearby. |

Regenerate only if the detector's contract changes. A fixture edit that makes a
near-miss file quarantinable is a behaviour change, not a fixture fix.
