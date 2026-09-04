// Purpose: the vault broker - the single owner of secret storage and
//
//	retrieval, sitting between the CLI/RPC surface and a Custody backend.
//
// Inputs: a Custody and an ElevationGate, both injected; the broker never
//
//	reaches for the environment itself. It holds no clock because nothing
//	it does is time-dependent: expiry lives in the elevation gate.
//
// Outputs: Get/Set/Rotate/List/Delete, plus the SetResult that tells a
//
//	caller which name a colliding Set landed under.
//
// Constraints: Get and Rotate are elevated verbs. The gate is consulted
//
//	BEFORE the store is touched, and a broker built without a gate refuses
//	every elevated verb: a nil gate is "no authorisation available", never
//	"no authorisation needed". No method returns a zero value in place of
//	an error, and no error, log line or event this package produces
//	carries a secret value.
//
// SPORT: internal/secrets Broker/ADDED.

package secrets

import (
	"context"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Verb names the broker operations the elevation gate classifies. They are
// the RPC method names from the CLI command tree, so the gate can consult
// the one canonical elevated-verb table rather than a second copy of it.
const (
	// VerbGet is the RPC method name for reading a secret value.
	VerbGet = "vault.get"
	// VerbRotate is the RPC method name for replacing a secret value.
	VerbRotate = "vault.rotate"
)

// ElevationGate authorises an elevated verb. Implementations return nil to
// allow and a KindElevationRequired (or KindUnsupported, on a tier-2
// platform) error to refuse.
//
// The broker deliberately does not implement this itself: deciding whether
// an operator has proved local presence is the elevation domain's job, and
// duplicating that decision here would give the repo two answers to the
// same question. cmd/cascade wires the production gate.
type ElevationGate interface {
	// Authorize reports whether verb may proceed now. It is called once
	// per elevated operation, before any store access.
	Authorize(ctx context.Context, verb string) error
}

// Broker is the vault's single entry point.
type Broker struct {
	custody Custody
	gate    ElevationGate
}

// NewBroker builds a broker over custody. A nil custody is refused rather
// than tolerated: a broker with no store would answer every List with an
// empty list, which reads exactly like a vault whose secrets were deleted.
func NewBroker(custody Custody, gate ElevationGate) (*Broker, error) {
	if custody == nil {
		return nil, ErrNoCustodyAvailable()
	}
	return &Broker{custody: custody, gate: gate}, nil
}

// Backend reports which custody answered, for diagnostics and for the CLI's
// "which store did this land in" reporting. Never a secret.
func (b *Broker) Backend() string { return b.custody.Name() }

// authorize runs the elevated-verb gate. A broker with no gate refuses:
// this is the fail-closed rule that keeps a partially-wired composition
// root from handing out secret values with no authorisation at all.
//
// The single-use attestation ledger check belongs here once the approval
// token layer lands; the gate interface is the seam it plugs into, and the
// production gate is the only thing that changes.
// CASCADE-ALLOW: approval-token ledger wiring is owned by the audit-domain
// ticket that follows this one; the ElevationGate seam is the open path.
func (b *Broker) authorize(ctx context.Context, verb string) error {
	if refusal := platformElevatedRefusal(); refusal != nil {
		return refusal
	}
	if b.gate == nil {
		return cascade.Newf(cascade.KindElevationRequired,
			"secrets: %s is an elevated verb and no elevation gate is configured; refusing", verb)
	}
	return b.gate.Authorize(ctx, verb)
}

// Get returns a secret's value. Elevated: the gate runs first, and a
// refusal never touches the store.
func (b *Broker) Get(ctx context.Context, name string) ([]byte, error) {
	if err := validateSecretName(name); err != nil {
		return nil, err
	}
	if err := b.authorize(ctx, VerbGet); err != nil {
		return nil, err
	}
	return b.custody.Get(ctx, name)
}

// SetResult reports what a Set did. Name is the name the value actually
// landed under, which differs from the requested name only when the caller
// asked for the auto-suffixed collision behaviour.
type SetResult struct {
	// Name is the name the value was stored under.
	Name string
	// Replaced reports whether an existing entry of that name was
	// overwritten.
	Replaced bool
}

// SetMode selects how Set handles an existing name.
type SetMode int

const (
	// SetUpdate overwrites the existing entry. This is the non-interactive
	// default and matches import's overwrite semantics.
	SetUpdate SetMode = iota
	// SetRename stores under the first free NAME_<n> suffix instead of
	// overwriting.
	SetRename
	// SetRefuse reports ErrSecretExists rather than writing anything.
	SetRefuse
)

// maxCollisionSuffix bounds the auto-rename search so a pathological vault
// cannot turn one Set into an unbounded scan.
const maxCollisionSuffix = 1000

// Set stores value under name. Set is NOT an elevated verb: writing a new
// secret does not disclose an existing one.
func (b *Broker) Set(ctx context.Context, name string, value []byte, mode SetMode) (SetResult, error) {
	if err := validateSecretName(name); err != nil {
		return SetResult{}, err
	}
	exists, err := b.Exists(ctx, name)
	if err != nil {
		return SetResult{}, err
	}
	target := name
	switch {
	case !exists:
	case mode == SetRefuse:
		return SetResult{}, ErrSecretExists(name)
	case mode == SetRename:
		if target, err = b.freeName(ctx, name); err != nil {
			return SetResult{}, err
		}
		exists = false
	}
	if err := b.custody.Set(ctx, target, value); err != nil {
		return SetResult{}, err
	}
	return SetResult{Name: target, Replaced: exists}, nil
}

// Exists reports whether a name is stored. It answers from the name list,
// never by reading a value, so it is not an elevated operation.
func (b *Broker) Exists(ctx context.Context, name string) (bool, error) {
	names, err := b.custody.List(ctx)
	if err != nil {
		return false, err
	}
	for _, existing := range names {
		if existing == name {
			return true, nil
		}
	}
	return false, nil
}

// freeName returns the first NAME_<n> that is not taken.
func (b *Broker) freeName(ctx context.Context, name string) (string, error) {
	for n := 2; n <= maxCollisionSuffix; n++ {
		candidate := name + "_" + strconv.Itoa(n)
		taken, err := b.Exists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", cascade.Newf(cascade.KindConflict,
		"secrets: %q and its first %d suffixed names are all taken", name, maxCollisionSuffix-1)
}

// Rotate replaces an existing secret's value. Elevated: rotating destroys
// the previous value, so it carries the same authorisation as reading one.
// A name that is not stored is a not-found refusal, never a silent create:
// a typo'd rotate must not quietly mint a new secret nobody asked for.
func (b *Broker) Rotate(ctx context.Context, name string, value []byte) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	if err := b.authorize(ctx, VerbRotate); err != nil {
		return err
	}
	exists, err := b.Exists(ctx, name)
	if err != nil {
		return err
	}
	if !exists {
		return ErrSecretNotFound(name)
	}
	return b.custody.Set(ctx, name, value)
}

// List returns the stored names, sorted. Names only: this method has no
// path that can reach a value, which is what makes it safe to expose.
func (b *Broker) List(ctx context.Context) ([]string, error) {
	return b.custody.List(ctx)
}

// Delete removes a secret.
func (b *Broker) Delete(ctx context.Context, name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	return b.custody.Delete(ctx, name)
}
