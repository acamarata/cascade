# Installation identity

The controller holds one Ed25519 key, the installation identity key. It
signs scope envelopes: the bounded scope a dispatch may act within. Node
enrollment pins its public half, so a node can refuse an envelope it did not
sign; that pinning lands with node controller designation, not here.

## What it signs

- Scope envelopes, through the one production `envelope.Signer` in the
  daemon. Every envelope it issues verifies only under the installation
  public key. An envelope signed by a node identity key does not verify,
  even when it names the controller as its issuer.
- It does not sign approvals. Approval records use a separate key
  (`cascade_approval_key`, see [approval records](approval-records.md)).
- Node identity keys stay per node. A controller that also enrolled itself
  as a node keeps that node key apart from this one.

## Where it lives

- In the vault, under the reserved name `cascade_installation_key`, in the
  same custody the daemon's outbound firewall uses (the OS keystore, or the
  encrypted file vault where there is none).
- Generated the first time a controller code path asks for the signer or
  the public key, only when custody reports the entry absent. An
  existing entry is read, never replaced. The absence check and the write
  hold the cross-process daemon-key lock, so two processes cannot both
  create it, and the daemon re-reads the key after writing it.
- A stored value that is not a well-formed Ed25519 private key, a vault file
  that does not decrypt, and a custody read that fails for any reason other
  than "not found" are errors. Nothing is generated or written over them, and
  no signer is built.
- The signer is built only when the key read back for signing is the key
  provisioning reported. A mismatch is an integrity error.
- The private half is read through one named, non-elevated reader, so a
  release build (which refuses elevated vault verbs) can still sign. No RPC
  returns it, and the signer never prints it.
- `vault set` in every collision mode, `vault rotate`, `vault delete`,
  `vault get` and the import paths refuse the reserved name before writing
  anything.

## Who may hold it

Only the controller. On every request for the signer or the public key, the
check below runs before anything else:

- the controller-binding entry under the data directory is proven absent
  (an entry of any kind there, a dangling link included, means "enrolled as
  a node"), and
- the node role resolver agrees the daemon is the controller.

Any probe error (permission denied, an I/O error, a path component that is
not a directory) is a refusal, never read as "no binding". So is an
unresolvable or relative data directory. A refusal is a permission error;
nothing is read from or written to the vault. The check is repeated on every
call: a daemon that becomes a node while running stops being handed the
signer.

A controller that ran `cascade node enroll` itself (it has a node identity
and a device record, but no controller binding) is still the controller.

## Who pins it

Node enrollment is where the installation public key gets pinned into node
bindings and where nodes start verifying envelopes against that pin. That
work, together with an explicit controller designation that replaces the
binding-file check above, is not part of this page's mechanism yet; until it
lands, nothing pins the key.

## Limits

- A node whose controller-binding file was deleted has no node-only marker
  left and passes the check above. Explicit controller designation closes
  this.
- Windows has no daemon (tier 2), so there is no installation key there.
- Nothing provisions the key at daemon start: the first caller that needs
  the signer or the public key provisions it.
