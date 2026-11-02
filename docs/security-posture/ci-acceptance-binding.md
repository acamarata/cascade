# CI acceptance binding

The controller issues one nonce before an acceptance re-run. The stored fact
binds the job, checkpoint, canonical repository registration ID, resolved job
spec hash, policy version and required plan kinds. Reusing the completed
checkpoint reuses that fact, including after the verifier restarts. A different
repository registration cannot reuse its nonce.

Every terminal local sub-job is read back from the CI result store. The
controller builds the attestation from those rows and the issued fact, signs
through its existing node identity in custody, verifies through the composed
controller and enrolled-node trust store, and persists the record. Identity or
signing failure produces no signed row and refuses acceptance. A node-executed
result cannot be signed using the controller's local executor path. A failed
local re-run may be stored, but cannot satisfy an acceptance gate.

Attestation evidence carries `ci_attestation:<row ID>` as its artifact reference.
Appending it requires a matching stored node, checkpoint, tree and issuing job.
The artifact reference participates in the evidence hash chain. Identity
prefixes alone are insufficient, including for legacy evidence.

High and Critical gate sets require that referenced attestation to verify under
current trust and derive an eligible class. The gate checks passing results for
every issued plan kind, the exact checkpoint and tree, all issued fact fields,
and the current stored job spec and dependency IDs. Stored class and exit status
columns are not trusted as proof. Missing trust, revoked keys and edited specs
refuse the gate. Low and Normal do not require attestation evidence.

The accepted state update and nonce consumption share one transaction. The
conditional state update must affect exactly one row, and the conditional nonce
update must consume exactly one previously unconsumed, unexpired nonce. A
rollback restores both state and nonce. A second consumption is refused.

Repository identities come from the shared context repository store. Only the
elevated local daemon registration handler signs operator registrations and
immutable baseline tips, using the installation identity. Trust-fetch records
store the fetcher's supplied tips, never inferred user-controlled refs. Every
registration, baseline and trust-fetch tip signature is checked on read under
the `cascade.trust.record/v1` domain; unsigned, modified and cross-repository
records fail closed. Default baselines consider only the checked-out branch,
apply the injected ownership predicate to raw and effective upstream URLs, and
exclude detached HEADs unless branches are explicitly named.

register elevation is defense in depth; the replay boundary is BindGate through elevated node.git_gate_install plus GateRepoID == fact.RepoID
