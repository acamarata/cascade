# internal/nodes testdata

## ssh tunnel real-counterpart provenance (S-36.T3, Art.2.2)

`TestTunnelRealSSHD` and `TestTunnelRealSSHD_ChangedHostKeyRefused`
(`../tunnel_test.go`, tagged `integration`) exercise the ssh tunnel
against a REAL sshd, never a self-authored dialect: OpenSSH's own
`/usr/sbin/sshd` binary, spawned as a subprocess on loopback
(`127.0.0.1`) at test run time.

- **Tool**: OpenSSH `sshd`, whatever version is installed on the host
  running the test (`/usr/sbin/sshd -V` prints it; the CI job
  `node-tunnel-real-sshd` in `.github/workflows/ci.yml` installs it from
  the `openssh-server` apt package on `ubuntu-latest` immediately before
  running the test, so the exact version is pinned by that runner image
  at CI run time, not by this repo).
- **Version captured at CI run time**: the CI job's `install
  openssh-server` step runs `/usr/sbin/sshd -V` and its output is in
  that job's own log for every run — there is no static version pinned
  here, since the point of this lane is exercising whatever real sshd
  the target platform ships.
- **Date**: this lane was added 2026-09-11 (P1-E17-W4-S36-T3).
- **No static fixture files.** The host key and the client (controller)
  identity key are both generated fresh, in memory, by each test run
  (`crypto/ed25519.GenerateKey` + `golang.org/x/crypto/ssh.MarshalPrivateKey`
  for the host key; `internal/nodes.GenerateIdentity` + the real
  `NodeKeystore` for the controller's own identity) — never committed,
  never reused across runs. This is deliberate: a committed key or
  fixture with real credential SHAPE is blocked by GitHub push
  protection, and reusing one across runs would make the "real sshd"
  proof stale the moment sshd's own defaults change.
- **`sshd/` directory**: kept empty (see `sshd/.gitkeep`) as the anchor
  files_scope names; the generated `sshd_config`, host key, and
  `authorized_keys` file all live under each test's own `t.TempDir()`
  instead, so nothing here goes stale or needs updating when the
  generated config's shape changes.

## minisign real-counterpart provenance (S-36.T5, Art.2.2)

`minisign/` holds a small, committed fixture set proving `ParseMinisignSignature`/
`VerifyMinisign` against a real `minisign` CLI, never a self-authored dialect.

- **Tool**: `minisign` 0.12 (Homebrew, `/opt/homebrew/bin/minisign`), run
  locally at fixture-generation time (2026-09-12, P1-E17-W4-S36-T5).
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
- **Why committed (unlike the ssh tunnel lane above)**: a minisign
  signature and public key carry no secret material and no shape a
  credential scanner recognizes — GitHub push protection does not block
  them, and a small deterministic fixture lets the unit-test suite (not
  just the `integration`-tagged lane) exercise the exact real wire format
  without invoking a subprocess on every run. The `integration`-tagged
  `TestVersionVerifyRealMinisign` and `TestProvisionRealSSHD` additionally
  generate a FRESH ephemeral keypair and sign a fresh artifact at test run
  time (mirroring the ssh tunnel lane's discipline above), so the
  real-counterpart proof does not rest solely on one committed fixture.
- **`fuzz/FuzzMinisignSignature/`**: three seeds — the real fixture above,
  an empty input, and a hand-shaped malformed line pair — for this
  ticket's mandated `FuzzMinisignSignature` target (06 §5.7).

## Existing fixtures

`fuzz/` (excluding `FuzzMinisignSignature/`, S-36.T5) and `trust/` predate
this ticket and are unrelated to the ssh tunnel or minisign; see their own
git history for provenance.
