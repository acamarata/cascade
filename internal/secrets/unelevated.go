// Purpose: the NAMED, non-elevated vault reads. Three of them, each with a
//
//	single stated purpose and a machine-enforced caller allowlist
//	(internal/build/arch_secrets_test.go). They exist because a release
//	binary cannot perform an elevated verb at all, so a value reachable
//	only through Broker.Get is unreadable in exactly the builds that
//	ship, and because an interactive attestation prompt per approval
//	signature or per outbound response is not a control anybody can
//	answer honestly at that rate.
//
// Inputs: a *Broker. Every reader goes through the package-internal
//
//	internalGet, so name validation and the custody backend remain the
//	only path to a value; no reader talks to a custody backend
//	directly and none can widen what internalGet already permits.
//
// Outputs: ApprovalKeyReader hands back the approval keypair for the
//
//	policy signer; InstallationKeyReader hands back the controller
//	installation key for the scope-envelope signer; EgressVault satisfies the egress engine's value source
//	so the firewall's exact-value substitution pass has something to
//	resolve against.
//
// Constraints: fail closed. A nil broker, a malformed stored key and an
//
//	unreadable entry are all errors, never a zero value that reads as
//	"there is nothing to protect". No value reaches an error string.
//
// SPORT: SECRETS_UNELEVATED_READ: ADD (internal/secrets ApprovalKeyReader,
//
//	EgressVault); InstallationKeyReader/ADDED (P1-SEC-40).

package secrets

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApprovalKeyName is the vault entry the Ed25519 approval signing key is
// stored under. It is a constant rather than a caller-supplied name so
// the allowlisted read cannot be pointed at an arbitrary secret.
const ApprovalKeyName = "cascade_approval_key"

// approvalKeyIDBytes is how many bytes of the public key digest form the
// key id. Eight bytes is enough to tell two rotations apart in a log line
// and short enough to stay readable; it identifies a key, never a value.
const approvalKeyIDBytes = 8

// DefaultVaultService is the keychain service / secret-service collection
// label every cascade vault entry is filed under.
//
// It lives here, next to the readers, because two independent composition
// roots now open the same store: the CLI's vault command tree and the
// daemon's outbound firewall. Two literals would let them drift into
// reading different keychains while both reported success, so the label
// has one home and the CLI's own constant is pinned to it by a test.
const DefaultVaultService = "cascade-vault"

// ApprovalKeyReader reads the approval signing key WITHOUT the elevated
// verb gate.
//
// The elevated Broker.Get is the human-facing "show me this secret"
// verb. Signing an approval record is neither human-facing nor
// occasional: it happens once per authorised action, inside the process,
// and a release binary refuses elevated verbs outright. Routing it
// through Get would make approvals unusable in development and impossible
// in a shipped build.
//
// The exemption is narrow by construction: this reader reads ONE fixed
// name, and internal/build/arch_secrets_test.go fails the build if a
// package outside its allowlist so much as names the constructor.
type ApprovalKeyReader struct {
	broker *Broker
}

// NewApprovalKeyReader builds the reader over broker. A nil broker is
// refused rather than tolerated: a reader with no store would answer
// every read with "no approval key", which reads exactly like a host that
// has not been provisioned yet and would send the signer down its
// unprovisioned path with no way to tell the two apart.
func NewApprovalKeyReader(broker *Broker) (*ApprovalKeyReader, error) {
	if broker == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"secrets: an approval key reader needs a vault broker")
	}
	return &ApprovalKeyReader{broker: broker}, nil
}

// ApprovalKey returns the key id and the private key bytes. The returned
// slice is a copy the caller owns and is expected to zero; nothing is
// retained here between calls.
//
// The stored value is the ed25519 private key in its 64-byte form. A
// stored value of any other length is an integrity failure, not something
// to pad or truncate into shape: a signer given a mangled key produces
// signatures nothing can verify, which is indistinguishable from an
// attacker having replaced the key.
func (r *ApprovalKeyReader) ApprovalKey(ctx context.Context) (string, ed25519.PrivateKey, error) {
	if r == nil || r.broker == nil {
		return "", nil, cascade.New(cascade.KindUnavailable,
			"secrets: no approval key reader configured")
	}
	return readDaemonPrivateKey(ctx, r.broker, ApprovalKeyName)
}

// readDaemonPrivateKey is the one non-elevated private-key read both named
// key readers share. It reads the reserved name through daemonKeyGet, so
// KindNotFound and custody errors come back unchanged; a stored value that
// is not exactly an ed25519 private key is zeroed and refused as
// KindIntegrity, never padded, truncated or read as "absent".
func readDaemonPrivateKey(ctx context.Context, b *Broker, name string) (string, ed25519.PrivateKey, error) {
	value, err := daemonKeyGet(ctx, b, name)
	if err != nil {
		return "", nil, err
	}
	if len(value) != ed25519.PrivateKeySize {
		zeroAll([][]byte{value})
		return "", nil, cascade.Newf(cascade.KindIntegrity,
			"secrets: the stored %s is %d bytes; an ed25519 private key is %d",
			name, len(value), ed25519.PrivateKeySize)
	}
	private := ed25519.PrivateKey(value)
	return approvalKeyID(private.Public().(ed25519.PublicKey)), private, nil
}

// InstallationKeyReader reads the controller installation identity key
// (the scope-envelope signing key) WITHOUT the elevated verb gate, for the
// same reason ApprovalKeyReader does: signing happens inside the daemon,
// per dispatch, and a release binary refuses elevated verbs outright.
//
// It is a distinct type with its own allowlist row in
// internal/build/arch_secrets_test.go, and it reads ONE fixed name.
type InstallationKeyReader struct {
	broker *Broker
}

// NewInstallationKeyReader builds the reader over broker. A nil broker is
// refused: a reader with no store would answer "no key", which reads like
// an unprovisioned host.
func NewInstallationKeyReader(broker *Broker) (*InstallationKeyReader, error) {
	if broker == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"secrets: an installation key reader needs a vault broker")
	}
	return &InstallationKeyReader{broker: broker}, nil
}

// InstallationKey returns the key id and the private key bytes of
// InstallationKeyName. The slice is a copy the caller owns and zeroes;
// nothing is retained here. A malformed entry is KindIntegrity with a nil
// key; a missing entry is KindNotFound; custody errors are unchanged.
func (r *InstallationKeyReader) InstallationKey(ctx context.Context) (string, ed25519.PrivateKey, error) {
	if r == nil || r.broker == nil {
		return "", nil, cascade.New(cascade.KindUnavailable,
			"secrets: no installation key reader configured")
	}
	return readDaemonPrivateKey(ctx, r.broker, InstallationKeyName)
}

// approvalKeyID derives the stable identifier for a public key. It is a
// digest of the PUBLIC half, so publishing it in a record or a log line
// discloses nothing the verifier does not already hold.
func approvalKeyID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return hex.EncodeToString(sum[:approvalKeyIDBytes])
}

// EgressVault is the value source the outbound firewall resolves against.
//
// The firewall's substitution pass has two halves: the detector redacts a
// string with credential SHAPE, and the exact-value pass replaces a
// string that IS a stored secret. Without a value source only the first
// half runs, so a stored secret with no credential shape crosses
// unredacted. Binding this reader is what makes the second half real.
//
// It is deliberately a distinct type from ApprovalKeyReader rather than
// one reader with two methods: the allowlists differ, and a single type
// would let a caller allowed one purpose reach the other.
type EgressVault struct {
	broker *Broker
}

// NewEgressVault builds the firewall's value source over broker.
func NewEgressVault(broker *Broker) (*EgressVault, error) {
	if broker == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"secrets: an egress vault needs a vault broker")
	}
	return &EgressVault{broker: broker}, nil
}

// List returns every stored name. Names only, exactly as Broker.List:
// this method has no path to a value.
func (v *EgressVault) List(ctx context.Context) ([]string, error) {
	if v == nil || v.broker == nil {
		return nil, cascade.New(cascade.KindUnavailable, "secrets: no egress vault configured")
	}
	names, err := v.broker.List(ctx)
	if err != nil {
		return nil, err
	}
	// The daemon's own keys are not egress material: never listed here.
	visible := names[:0:0]
	for _, name := range names {
		if !reservedDaemonKey(name) {
			visible = append(visible, name)
		}
	}
	return visible, nil
}

// Get returns one stored value without the elevated gate.
//
// An outbound response is filtered on the daemon's own path with no
// operator present; raising an attestation prompt there would either
// block the response forever or, in a release build, refuse it outright,
// and a firewall that cannot read the vault cannot redact what is in it.
func (v *EgressVault) Get(ctx context.Context, name string) ([]byte, error) {
	if v == nil || v.broker == nil {
		return nil, cascade.New(cascade.KindUnavailable, "secrets: no egress vault configured")
	}
	return internalGet(ctx, v.broker, name)
}
