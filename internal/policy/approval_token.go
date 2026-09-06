// Package policy (approval_token.go): Purpose: the approval token's crypto
//
//	half — Ed25519 signing over the domain-separated canonical record, the
//	stateless verifier, and the request_id-only shape a bridge carries.
//
// Inputs: an ApprovalKeySource for the signing key, an injected Clock for
//
//	issue and expiry, and untrusted bytes on the verify path.
//
// Outputs: ApprovalSigner, ApprovalVerifier, BridgeRef, BridgePayload,
//
//	ErrExpired, ErrInvalidSignature. The §5.24 five-minute ceiling is
//	approval_queue.go's MaxApprovalTTL; this file reuses it rather than
//	declaring a second copy of the same number.
//
// Constraints: FAIL CLOSED at every door. Verify decodes strictly, checks
//
//	expiry against the injected clock, and only then checks the signature;
//	bytes it cannot account for REFUSE. The signing input is
//	domain-separated. Verify is STATELESS: replay is the redemption
//	ledger's job and this file ships no ledger. The private key is zeroed
//	the moment Sign returns.
//
// SPORT: internal/policy ApprovalSigner/ADDED, ApprovalVerifier/ADDED,
//
//	BridgePayload/ADDED, ErrExpired/ADDED, ErrInvalidSignature/ADDED
//	(P1-E08-W2-S16-T3).
package policy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApprovalKeyName is the vault key the Ed25519 approval keypair lives
// under.
const ApprovalKeyName = "cascade.approval.ed25519"

// approvalDomain is the domain-separation context string. It prefixes
// every signing input, so a signature produced here can never verify as
// anything else and a signature produced elsewhere can never verify here.
const approvalDomain = "cascade/approval-token/v1\x00"

// The stable identifier strings for this file's refusals (R-14.152).
const (
	// CodeApprovalExpired marks a token past its expiry.
	CodeApprovalExpired = "approval-expired"
	// CodeApprovalBadSignature marks a token whose signature does not
	// check out over its own bytes.
	CodeApprovalBadSignature = "approval-bad-signature"
)

// The comparison targets for this file's refusals.
var (
	// ErrExpired is returned for a token past its expiry, whether or not
	// its signature is good.
	ErrExpired = errors.New(CodeApprovalExpired)
	// ErrInvalidSignature is returned for any token whose bytes do not
	// verify, including one that could not be decoded at all.
	ErrInvalidSignature = errors.New(CodeApprovalBadSignature)
)

// ApprovalKeySource hands over the Ed25519 approval keypair. It is the
// seam the vault sits behind, and it exists so this package never imports
// the secrets domain and never raises an interactive attestation prompt
// per signature (R-21.229): the composition root supplies a source backed
// by the vault's NON-elevated internal read path, never by the elevated
// Get verb.
type ApprovalKeySource interface {
	// ApprovalKey returns the key id and the private key bytes. The
	// caller zeroes the returned slice; an implementation must therefore
	// hand back a copy it does not retain.
	ApprovalKey(ctx context.Context) (keyID string, private ed25519.PrivateKey, err error)
}

// ApprovalSigner mints signed approval records. It holds no key material
// between calls: the key is fetched, used and zeroed inside Sign.
type ApprovalSigner struct {
	keys  ApprovalKeySource
	clock Clock
}

// NewApprovalSigner builds a signer over keys, timing records with clock.
// Both are required: a signer with no key source could not sign, and one
// with no clock could not bound what it signed.
func NewApprovalSigner(keys ApprovalKeySource, clock Clock) (*ApprovalSigner, error) {
	if keys == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval signer requires a key source")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval signer requires a clock")
	}
	return &ApprovalSigner{keys: keys, clock: clock}, nil
}

// Sign fills in the record's identity, timing and key fields and returns
// canonical-record-bytes || signature.
//
// The caller does not choose the request id, the nonce, the issue time or
// the expiry: all four are set here, because every one of them is a thing
// a caller could weaken. The expiry is clamped to MaxApprovalTTL.
func (s *ApprovalSigner) Sign(ctx context.Context, rec ApprovalRecord) ([]byte, error) {
	keyID, private, err := s.keys.ApprovalKey(ctx)
	if err != nil {
		return nil, err
	}
	defer zeroKey(private)
	if len(private) != ed25519.PrivateKeySize {
		return nil, cascade.Newf(cascade.KindInternal,
			"policy: the value under %s is not an Ed25519 private key", ApprovalKeyName)
	}
	if rec.RequestID, err = cascade.NewID(); err != nil {
		return nil, err
	}
	if rec.Nonce, err = cascade.NewID(); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	rec.SchemaVersion = ApprovalSchemaVersion
	rec.KeyID = keyID
	rec.Issued = now
	rec.Expiry = now.Add(MaxApprovalTTL)
	payload := CanonicalEncode(rec)
	sig := ed25519.Sign(private, signingInput(payload))
	return append(append([]byte{}, payload...), sig...), nil
}

// zeroKey overwrites key material in place. Sign defers it so the bytes
// are gone whether Sign returned a token or an error.
func zeroKey(k ed25519.PrivateKey) {
	for i := range k {
		k[i] = 0
	}
}

// signingInput is the domain-separated byte string a signature covers.
func signingInput(payload []byte) []byte {
	return append([]byte(approvalDomain), payload...)
}

// ApprovalVerifier checks signed approval records. It is STATELESS: it
// decodes, checks expiry and checks the signature, and it consults nothing
// else. Single-use enforcement is the redemption ledger's job.
type ApprovalVerifier struct {
	public ed25519.PublicKey
	clock  Clock
}

// NewApprovalVerifier builds a verifier for one public key. A key of the
// wrong length is refused at construction rather than causing every later
// Verify to fail for a reason that looks like a bad token.
func NewApprovalVerifier(public ed25519.PublicKey, clock Clock) (*ApprovalVerifier, error) {
	if len(public) != ed25519.PublicKeySize {
		return nil, cascade.New(cascade.KindInvalidInput,
			"policy: approval verifier requires an Ed25519 public key")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval verifier requires a clock")
	}
	return &ApprovalVerifier{public: public, clock: clock}, nil
}

// Verify decodes and checks signed, returning the record it carries. Every
// refusal path returns a nil record: bytes too short to hold a signature,
// bytes that are not the canonical encoding of a record, an unknown schema
// version, an expired record and a bad signature all refuse.
func (v *ApprovalVerifier) Verify(signed []byte) (*ApprovalRecord, error) {
	if len(signed) <= ed25519.SignatureSize {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSignature,
			"policy: approval token is too short to carry a signature")
	}
	split := len(signed) - ed25519.SignatureSize
	payload, sig := signed[:split], signed[split:]
	rec, err := canonicalDecode(payload)
	if err != nil {
		return nil, err
	}
	if rec.SchemaVersion != ApprovalSchemaVersion {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSignature,
			"policy: approval token names schema version %d, which this build does not verify",
			rec.SchemaVersion)
	}
	if !v.clock.Now().Before(rec.Expiry) {
		return nil, cascade.Wrapf(cascade.KindPolicyDenied, ErrExpired,
			"policy: approval token expired at %s", canonicalTime(rec.Expiry))
	}
	if !ed25519.Verify(v.public, signingInput(payload), sig) {
		return nil, cascade.Wrapf(cascade.KindPolicyDenied, ErrInvalidSignature,
			"policy: approval token signature does not verify")
	}
	return &rec, nil
}

// BridgeRef is the ONLY shape an approval may take across a bridge
// (§5.24): the request id and nothing else. The token, the nonce, the
// parameter digest and the verb all stay on the controller, which looks
// the request up and verifies it server-side.
type BridgeRef struct {
	// RequestID is the queued action's identifier.
	RequestID cascade.ID `json:"request_id"`
}

// BridgePayload projects a record down to what a bridge may carry. It is a
// projection rather than a redaction: the returned struct has one field,
// so there is no path by which a later edit to ApprovalRecord widens what
// crosses a bridge.
func BridgePayload(rec ApprovalRecord) BridgeRef {
	return BridgeRef{RequestID: rec.RequestID}
}

// rejectDuplicateKeys walks raw as a token stream and refuses any object
// that names the same key twice. encoding/json keeps the last value for a
// repeated key, which means two readers of the same bytes can disagree
// about what they say; for a signed record that is a forgery primitive,
// not a formatting quirk.
func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var stack []*jsonFrame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSignature,
				"policy: approval token is not well-formed JSON")
		}
		if delim, ok := tok.(json.Delim); ok {
			if delim == '}' || delim == ']' {
				stack = stack[:len(stack)-1]
				continue
			}
			noteValue(stack)
			stack = append(stack, &jsonFrame{object: delim == '{', wantKey: delim == '{'})
			continue
		}
		if err := noteScalar(stack, tok); err != nil {
			return err
		}
	}
}

// jsonFrame tracks one open container while the duplicate-key scan walks a
// document: whether it is an object, the keys it has already named, and
// whether the next token in it is a key or a value.
type jsonFrame struct {
	object  bool
	wantKey bool
	seen    map[string]bool
}

// noteValue records that a value has just been consumed in the innermost
// container, so an object expects a key next.
func noteValue(stack []*jsonFrame) {
	if len(stack) == 0 {
		return
	}
	if top := stack[len(stack)-1]; top.object {
		top.wantKey = true
	}
}

// noteScalar classifies one scalar token as either a key (checked for
// repetition) or a value.
func noteScalar(stack []*jsonFrame, tok json.Token) error {
	if len(stack) == 0 {
		return nil
	}
	top := stack[len(stack)-1]
	if !top.object || !top.wantKey {
		noteValue(stack)
		return nil
	}
	name, ok := tok.(string)
	if !ok {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSignature,
			"policy: approval token names a non-string object key")
	}
	if top.seen == nil {
		top.seen = map[string]bool{}
	}
	if top.seen[name] {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrDuplicateKey,
			"policy: approval token repeats a key")
	}
	top.seen[name] = true
	top.wantKey = false
	return nil
}
