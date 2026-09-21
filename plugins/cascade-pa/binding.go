// Purpose: the bridge's Q-identity binding — which subject (bridge
//   instance) is paired, which sender ids that pairing admits, and the
//   Q/S-36.T1 device-record cross-registration every Bind performs.
//
// Inputs: a subject id, a sender id the bridge's own transport reports
//   (Telegram from.id as a string), a PairClock, a DeviceRegistrar the
//   host supplies over internal/nodes, and the BridgeState store.
//
// Outputs: Bind records a paired-device binding durably and returns it;
//   IsAllowed answers the dispatch gate's fail-closed question from the
//   PERSISTED row, so a restart never re-opens a bound bot to strangers
//   and never forgets an owner.
//
// Constraints (R-21.227, and the ticket's PAIRING BINDING SEMANTICS,
//   quoted because it is load-bearing):
//   - trustTierPairedDevice is a LOCAL STRING CONSTANT, not
//     internal/nodes.TierPairedDevice: plugins/cascade-pa never imports
//     internal/ (Art.10.2, R-14.69), so the two independently equal
//     "paired-device" rather than sharing a Go identifier.
//   - "It is NOT a node enrollment: it does not invoke the Q/S-36.T1
//     enrollment elevation gate and grants no node-dispatch capability."
//     Nothing here can reach an enrollment path; the host's registrar
//     writes a device record whose trust tier carries no dispatch right.
//   - REGISTRATION FAILURE IS A BIND FAILURE. The earlier draft discarded
//     the registrar's error with `_, _ =`, so Bind reported success while
//     the wider device registry recorded nothing. A binding the node
//     registry never heard of is exactly the half-written state the
//     ticket's "written atomically to the daemon store" forbids, so the
//     registrar runs FIRST and a refusal aborts the bind with nothing
//     written anywhere.
//
// SPORT: plugins/cascade-pa BindingStore/ADDED, DeviceRegistrar/ADDED,
//   Stores/ADDED (P1-E23-W5-S48-T1).

package cascadepa

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// trustTierPairedDevice mirrors internal/nodes.TierPairedDevice's string
// value (06 §5.22) without importing internal/nodes — see this file's doc
// comment.
const trustTierPairedDevice = "paired-device"

// Binding is one subject's pairing state as a caller sees it.
type Binding struct {
	Subject     string
	TrustTier   string
	PairedAt    time.Time
	AllowedFrom []string
}

// BindingStore reads and writes bindings through BridgeState.
type BindingStore struct {
	clock     PairClock
	state     BridgeState
	registrar DeviceRegistrar
	// locks is the per-subject mutex table; NewStores replaces it with the
	// one the whole bundle shares, so a Bind cannot interleave with the poll
	// loop's UpdateLedger.Accept and have its AllowedFrom reverted.
	locks *subjectLocks
}

// NewBindingStore constructs a store. A nil registrar or state resolves to
// the fail-closed default for that seam, never to a permissive one.
func NewBindingStore(clock PairClock, registrar DeviceRegistrar, state BridgeState) *BindingStore {
	if registrar == nil {
		registrar = unconfiguredDeviceRegistrar{}
	}
	return &BindingStore{
		clock: clock, state: orUnconfigured(state),
		registrar: registrar, locks: newSubjectLocks(),
	}
}

// Stores bundles this ticket's producer stores for one composition-root
// call, so a host builds all of them over one clock, one registrar and one
// durable state store rather than assembling them separately and getting
// three different notions of where the truth lives.
type Stores struct {
	Pairing  *PairCodeStore
	Binding  *BindingStore
	Callback *CallbackNonceStore
	Updates  *UpdateLedger
	Clock    PairClock
}

// NewStores constructs every bridge store over one clock/registrar/state and
// one pairing-code key (DerivePairCodeKey of the bridge's own credential).
//
// It also gives all three row-mutating stores ONE per-subject lock table. That
// sharing is the point of the constructor: assembling the stores separately
// gives each its own locks, and then issuance, binding and the poll loop
// serialise against themselves but not against each other — which is exactly
// the interleaving that reverted an offset and, around a Bind, silently
// unpaired a bound bot.
func NewStores(clock PairClock, registrar DeviceRegistrar, state BridgeState, pairKey []byte) *Stores {
	locks := newSubjectLocks()
	pairing := NewPairCodeStore(clock, state, pairKey)
	binding := NewBindingStore(clock, registrar, state)
	updates := NewUpdateLedger(state)
	pairing.locks, binding.locks, updates.locks = locks, locks, locks
	return &Stores{
		Pairing:  pairing,
		Binding:  binding,
		Callback: NewCallbackNonceStore(),
		Updates:  updates,
		Clock:    clock,
	}
}

// Bind records subject as paired and admits senderID.
//
// Calling it again for an already-bound subject ADDS senderID to the
// existing allowlist (R-21.227's per-user allowlist: a bound bot may admit
// more than one Telegram user) rather than replacing the binding.
func (s *BindingStore) Bind(ctx context.Context, subject, senderID string) (Binding, error) {
	if subject == "" || senderID == "" {
		return Binding{}, cascade.New(cascade.KindInvalidInput,
			"cascade-pa: bind requires a subject and sender id")
	}
	return mutate(s.locks, subject, func() (Binding, error) { return s.bindOnce(ctx, subject, senderID) })
}

// bindOnce is Bind's read-modify-write, run under the subject lock and
// retried once by mutate on a compare-and-swap refusal. It re-Loads on every
// attempt, so the retry adds the sender to the CURRENT allowlist rather than
// writing back a copy that predates another writer.
func (s *BindingStore) bindOnce(ctx context.Context, subject, senderID string) (Binding, error) {
	st, _, err := s.state.Load(ctx, subject)
	if err != nil {
		return Binding{}, err
	}
	// The wider Q/S-36.T1 device record is written BEFORE the local row,
	// so a registrar refusal leaves nothing bound at all.
	if _, rerr := s.registrar.RegisterPairedDevice(ctx, subject, senderID, s.clock.Now()); rerr != nil {
		return Binding{}, cascade.Wrapf(cascade.KindUnavailable, rerr,
			"cascade-pa: refusing to bind %s: the paired-device record was not written", subject)
	}
	st.Subject = subject
	st.TrustTier = trustTierPairedDevice
	st.PairedAt = s.clock.Now()
	st.admit(senderID)
	if err := s.state.Save(ctx, st); err != nil {
		return Binding{}, err
	}
	return bindingOf(st), nil
}

// bindingOf projects a persisted row onto the caller-facing Binding.
func bindingOf(st SubjectState) Binding {
	out := Binding{
		Subject:     st.Subject,
		TrustTier:   st.TrustTier,
		PairedAt:    st.PairedAt,
		AllowedFrom: make([]string, len(st.AllowedFrom)),
	}
	copy(out.AllowedFrom, st.AllowedFrom)
	return out
}

// IsAllowed answers the dispatch gate's fail-closed question: is subject
// bound at all, and is senderID on its allowlist? An unbound subject, a
// bound-but-non-allowlisted sender and an unreadable store all answer
// false, which is what makes those cases indistinguishable to a sender.
// The error is returned as well so a caller can log WHY without ever
// telling the sender.
func (s *BindingStore) IsAllowed(ctx context.Context, subject, senderID string) (bool, error) {
	st, ok, err := s.state.Load(ctx, subject)
	if err != nil {
		return false, err
	}
	if !ok || !st.Bound() {
		return false, nil
	}
	return st.Allows(senderID), nil
}

// Bound reports whether subject has any binding at all — the question
// "/pair on a bound bot" has to answer before it decides whether a code is
// even redeemable (R-21.227, T0 decision D5).
func (s *BindingStore) Bound(ctx context.Context, subject string) (bool, error) {
	st, ok, err := s.state.Load(ctx, subject)
	if err != nil {
		return false, err
	}
	return ok && st.Bound(), nil
}

// DeviceRegistrar writes the Q/S-36.T1 paired-device record for a bridge
// binding. The host implements it over internal/nodes' RecordStore; this
// package cannot import that (Art.10.2), which is why it is an interface
// here and a real store there.
type DeviceRegistrar interface {
	// RegisterPairedDevice records subject/senderID as a paired device and
	// returns the device record's node id. An error aborts the bind.
	RegisterPairedDevice(ctx context.Context, subject, senderID string, pairedAt time.Time) (nodeID string, err error)
}

// ErrNoDeviceRegistrar is the fail-closed refusal an unwired host gets.
// Exported so a host wiring test can assert the default by identity.
var ErrNoDeviceRegistrar = cascade.New(cascade.KindUnavailable,
	"cascade-pa: no paired-device registrar is wired; the bridge refuses to bind")

type unconfiguredDeviceRegistrar struct{}

func (unconfiguredDeviceRegistrar) RegisterPairedDevice(
	context.Context, string, string, time.Time,
) (string, error) {
	return "", ErrNoDeviceRegistrar
}
