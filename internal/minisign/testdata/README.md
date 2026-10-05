# internal/minisign testdata

Moved from `internal/nodes/testdata` when the verifier moved to this package; the provenance below is unchanged.

## minisign real-counterpart provenance (Art.2.2)

`minisign/` holds a small, committed fixture set proving `ParseSignature`/
`Verify` against a real `minisign` CLI, never a self-authored dialect.

- **Tool**: `minisign` 0.12 (Homebrew, `/opt/homebrew/bin/minisign`), run
  locally at fixture-generation time (2026-09-12).
- **Files**: `artifact.bin` (the signed message, `"hello world content\n"`),
  `artifact.bin.minisig` (the real detached signature, default `ED`
  BLAKE2b-512-prehashed algorithm, trusted comment `"test comment v1"`),
  `test.pub` (the corresponding public key). The matching SECRET key was
  generated with `minisign -G -p test.pub -s test.key -W` into a
  throwaway `/tmp` directory and never written anywhere under this repo —
  only the public artifacts a verifier needs are committed.
- **Generation**: `minisign -S -s test.key -m artifact.bin -x
  artifact.bin.minisig -t "test comment v1" -W`. Independently
  cross-verified with `minisign -V -p test.pub -m artifact.bin -x
  artifact.bin.minisig` ("Signature and comment signature verified")
  before being committed.
- **Why committed (unlike the ssh tunnel fixtures in internal/nodes)**: a minisign
  signature and public key carry no secret material and no shape a
  credential scanner recognizes — GitHub push protection does not block
  them, and a small deterministic fixture lets the unit-test suite (not
  just the `integration`-tagged lane) exercise the exact real wire format
  without invoking a subprocess on every run. The `integration`-tagged
  `TestVersionVerifyRealMinisign` (this package) and `TestProvisionRealSSHD` (internal/nodes) additionally
  generate a FRESH ephemeral keypair and sign a fresh artifact at test run
  time (mirroring the ssh tunnel lane's discipline above), so the
  real-counterpart proof does not rest solely on one committed fixture.
- **`fuzz/FuzzMinisignSignature/`**: three seeds — the real fixture above,
  an empty input, and a hand-shaped malformed line pair — for this
  ticket's mandated `FuzzMinisignSignature` target (06 §5.7).
