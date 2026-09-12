# fixture.age -- provenance

Captured from the REAL reference `age` CLI (Art.2: never a self-authored
dialect). Recorded verbatim from the terminal session that produced it.

- Tool: `age` (https://github.com/FiloSottile/age), Homebrew formula.
- Version: `age -v` reported `v1.3.2`.
- Date captured: 2026-09-12.
- Passphrase: `cascade-recovery-key-fixture`.
- Plaintext payload: one `age-keygen`-generated X25519 identity string
  (`AGE-SECRET-KEY-1...`), the same shape this ceremony wraps.

## How it was produced

```
$ age-keygen -o /tmp/test-identity.txt
Public key: age1cyha9tllf52asvcw9r8dsy8hgsq4599atjwmqzd9h9lf3964zy2qmygl4g

$ grep AGE-SECRET-KEY /tmp/test-identity.txt > /tmp/plain-identity.txt

$ age -p -a -o fixture.age /tmp/plain-identity.txt
Enter passphrase (leave empty to autogenerate a secure one): cascade-recovery-key-fixture
Confirm passphrase: cascade-recovery-key-fixture
```

(The passphrase prompts were driven non-interactively via `expect`, since
`age` refuses passphrase mode with no controlling TTY; the two invocations
above are the exact commands and inputs, run back to back in the same
session.)

## How it was verified

Decrypted back with the same real CLI immediately after capture, and the
recovered identity string was byte-for-byte diffed against the original
plaintext file:

```
$ age -d -o /tmp/decrypted.txt fixture.age
Enter passphrase: cascade-recovery-key-fixture
$ diff /tmp/decrypted.txt /tmp/plain-identity.txt
(no output -- identical)
```

`TestRecoveryKey_RealAgeCLIFixtureDecrypts` (recovery_test.go) then proves
this repository's own `UnwrapRecoveryKey` decrypts the identical bytes
with the identical passphrase, which is the external-contract assertion
this ticket requires: the wire format this package writes and reads is
the real age.filippo.io/age format, not a private dialect that happens to
resemble it.

The identity's corresponding secret key material has no other use; it was
generated solely to produce this fixture and is not used anywhere else in
this repository or in production.
