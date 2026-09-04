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
