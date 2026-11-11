# Receive gate

The receive-gate contract protects ref updates at the receiving repository.
Its elevated `cascade node git-gate install [--repo <path>] --repo-id <id>`
binds the canonical Git common directory to an existing registry identity
before writing either the pre-receive hook or `cascade.gate.repoId`. A refused
binding leaves those files untouched. The receiving process computes the gate
directory from its own cwd/GIT_DIR, never from Git config. Missing config,
unknown registrations, or a config id that disagrees with the controller's
binding refuse. Distinct bare repositories retain distinct gate bindings even
when they share a parent directory. No history lookup is needed for identity,
so shallow repositories work too.

## Evidence checked for each tip

The gate requires at least one stored row that meets all of these checks:

- The canonical signed record verifies under the signer's current, unrevoked
  key and binds the requested commit SHA and tree hash.
- Its proof-derived attestor class is eligible. `unattested` and `unverified`
  evidence never authorizes a push.
- The record is unexpired with a lifetime no greater than 24 hours. The run's
  exit status is zero and its result hashes cover every accepted plan kind.
- An acceptance fact exists for the nonce, commit, tree and checkpoint.
  The record's repository id, checkpoint id, job specification hash and policy
  version equal the acceptance fact's values. These checks compare independent
  evidence, never a record against itself.
- The fact's repository id equals the receiving gate's repository id. An
  accepted passing run for registration A cannot be replayed at registration B,
  even for an identical commit SHA and tree.
- On a controller, the fact's policy version equals the current policy version;
  a policy read error refuses. On a node, the verified controller-signed fact's
  version is authoritative and `[nodes.repos]` restricts repository membership.

Verification reads acceptance evidence. It never issues or consumes a nonce;
consumption belongs to the controller's acceptance transaction. Missing
required evidence, signature failure and ineligible or unbound evidence are
reported through `ErrNoAttestation`, `ErrVerifyFailed` and
`ErrUnattestedEvidence`, respectively.

Malformed pre-receive input, ref deletion, non-fast-forward updates and moving
an existing tag refuse before attestation verification. New tips are always
verified, including commits already present in the receiver. Annotated tags
verify their peeled commit. Ancestors are covered by the attested tip.

## Hook integrity and operation

The installation contract pins an absolute executable path and its SHA-256
digest. The hook never resolves the executable through PATH and contains no
credentials, signing material or credential-store references. It uses
`/usr/bin/shasum -a 256` on macOS or `/usr/bin/sha256sum` on Linux. Binary drift
refuses with a reinstall instruction. Re-run install after
`cascade self-update`. Installation is idempotent for identical rendered hook
bytes and is unsupported on Windows.

The status contract, `cascade node git-gate status [--repo <path>] [--json]`,
reports hook installation and digest, binary path and drift, repository id and
binding, mirror enablement, last mirror push outcome and backlog without writes.
The hook process must have a current policy-version reader on a controller;
absence or failure must refuse rather than skip the policy comparison.

## Same-uid residual (M6)

The registry binding prevents a Git config edit from redirecting a controller
gate to another registered repository. It does not isolate the jobs database
from its owning OS user. A direct same-uid edit of that database can still
rebind a gate. Protect the receiving account, database and hook files at the OS
boundary; the receive gate does not provide a security boundary against their
owner.
