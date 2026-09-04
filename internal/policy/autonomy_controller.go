// Package policy (autonomy_controller.go): Purpose: resolution and the
//
//	running-profile holder — Resolve, which turns a parsed Config
//	into an immutable AutonomyProfile, and Controller, the hot-reload seam
//	that swaps the running profile atomically and emits the audit events
//	08 §3 requires.
//
// Inputs: a Config (autonomy_config.go), a config tree on reload, and
//
//	an optional AuditEmitter.
//
// Outputs: a resolved *AutonomyProfile, the approval numerics, and audit
//
//	events on load and on rejection.
//
// Constraints: RESTRICT-ONLY and NO CACHE. An overlay may only tighten the
//
//	slot its profile already holds; an overlay that would widen one is a
//	refusal, so the layer that decides "how much may happen without asking"
//	can never be used to grant more than the named profile did. Like the
//	grant model next door, nothing here memoises a decision: every read
//	loads the current pointer, so a swap takes effect on the very next call
//	and there is no cache for a stale profile to survive in. A rejected
//	reload leaves the running profile exactly as it was.
//
// SPORT: internal/policy Controller/ADDED, Resolve/ADDED,
//
//	AuditEmitter/ADDED (P1-E09-W2-S18-T1).
package policy

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Audit event names emitted on the two profile transitions. The rejection
// name is the one 08 §3's hot-reload rules fix for every section, so the
// operator sees one vocabulary whichever section refused the file.
const (
	// EventAutonomyProfileLoaded marks a profile becoming the running one.
	EventAutonomyProfileLoaded = "policy.autonomy.loaded"
	// EventConfigReloadRejected marks a config that was refused, with the
	// running profile left in place.
	EventConfigReloadRejected = "config.reload.rejected"
)

// AuditEmitter records one structured event. It returns an error so a real
// audit sink can report a write failure, but the callers here deliberately
// ignore it: the audit domain (S-18.T2) is not wired yet, and a missing
// audit sink must not stop a valid policy from loading. That is an
// allowed-fail leg, not a swallowed error — the event carries no
// information the decision path depends on.
type AuditEmitter func(ctx context.Context, event string, fields map[string]interface{}) error

// defaultProfileName is what an absent autonomy_profile key resolves to:
// the §5.15 baseline. It is the most permissive table that exists, and it
// still asks at L2/L3 and denies at L4 — there is no "full autonomy" name
// an operator can reach by typo or by omission.
const defaultProfileName = "balanced"

// Resolve turns a parsed Config into the immutable profile the
// engine reads.
//
// Two refusals matter here and neither degrades to a default:
//   - an unknown profile name is an error, never a fallback to balanced;
//   - an overlay that would make a slot LESS restrictive than its profile
//     already had it is an error, so the config surface cannot be used to
//     widen the table it names.
func Resolve(cfg Config) (*AutonomyProfile, error) {
	name := cfg.ProfileName
	if name == "" {
		name = defaultProfileName
	}
	factory, ok := builtinProfiles[name]
	if !ok {
		return nil, newConfigError("policy.autonomy_profile",
			"%q is not a profile (want %s)", sanitize(name), builtinProfileNames())
	}
	p := factory()
	p.name = name
	if err := applyOverlays(p, cfg.Overlays); err != nil {
		return nil, err
	}
	return p, nil
}

// builtinProfileNames renders the selectable names for an error message,
// in a stable order.
func builtinProfileNames() string {
	names := make([]string, 0, len(builtinProfiles))
	for n := range builtinProfiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// applyOverlays writes the overlay lists onto p, refusing any that widens.
//
// AutoAdvance is never re-enabled by an overlay: it is carried over from
// the profile default and then re-clamped, so an overlay can switch a
// level off auto-advance (by tightening its verdict) but cannot switch one
// on.
func applyOverlays(p *AutonomyProfile, overlays map[Verdict][]RiskLevel) error {
	for _, v := range []Verdict{VerdictAllow, VerdictAsk, VerdictDeny} {
		for _, level := range overlays[v] {
			lvl := safeLevel(level)
			base := p.slots[lvl]
			if maxVerdict(v, base.Verdict) != safeVerdict(v) {
				return newConfigError("policy."+v.String(),
					"%s would widen %s from %q to %q; overlays may only tighten",
					lvl.String(), p.name, base.Verdict.String(), v.String())
			}
			p.slots[lvl] = clampSlot(lvl, Slot{
				Verdict: v,
				// Carried, never granted: an overlay that tightens a
				// slot past the allow tier drops auto-advance with it,
				// and one that leaves the verdict alone keeps whatever
				// the profile default had.
				AutoAdvance: base.AutoAdvance && autoAdvanceEligible(lvl, v),
				Source:      SourceOverlay,
			})
		}
	}
	return nil
}

// Controller holds the running profile and the approval numerics, and is
// the seam the hot-reload engine (C/S-05.T8) drives.
//
// The zero Controller is usable and denies everything: no profile has been
// loaded, so Profile returns nil and a nil profile denies at every level.
// That is the state a daemon is in before its first config load, and it is
// deliberately the strictest one rather than the friendliest.
type Controller struct {
	emit     AuditEmitter
	profile  atomic.Pointer[AutonomyProfile]
	batching atomic.Pointer[ApprovalBatching]
}

// NewController returns a Controller with no profile loaded yet. emit may
// be nil, which discards events.
func NewController(emit AuditEmitter) *Controller {
	return &Controller{emit: emit}
}

// Profile returns the running profile, or nil when none has been loaded.
// Callers do not need to nil-check the result: every AutonomyProfile
// method is defined on the pointer receiver and treats nil as
// deny-everything.
func (c *Controller) Profile() *AutonomyProfile {
	if c == nil {
		return nil
	}
	return c.profile.Load()
}

// VerdictFor returns the running profile's verdict for level. It is the
// single autonomy source the policy engine consults (R-14.26 layer 5), and
// it reads the pointer on every call — there is no memoised verdict, so a
// profile change is visible to the very next decision.
func (c *Controller) VerdictFor(level RiskLevel) Verdict {
	return c.Profile().VerdictFor(level)
}

// Batching returns the approval numerics S-18.T3 reads. Before any config
// has been applied it returns the 08 §3 defaults: unlike a verdict, a
// batching window has no permissive failure mode — it decides only how
// approvals are grouped, never whether one is required.
func (c *Controller) Batching() ApprovalBatching {
	if c == nil {
		return defaultApprovalBatching()
	}
	if b := c.batching.Load(); b != nil {
		return *b
	}
	return defaultApprovalBatching()
}

// Apply parses, resolves and installs the [policy] section of tree.
//
// On success the profile and the batching numerics are swapped atomically
// and EventAutonomyProfileLoaded is emitted. On any failure NOTHING is
// swapped — the running profile and numerics stay exactly as they were —
// EventConfigReloadRejected is emitted, and the error is returned for the
// reload engine to surface. A rejected reload therefore cannot loosen a
// running system, and cannot leave it half-configured either.
func (c *Controller) Apply(ctx context.Context, tree map[string]interface{}) error {
	if c == nil {
		return cascade.New(cascade.KindInvalidInput, "policy: nil autonomy controller")
	}
	cfg, err := ParseConfig(tree)
	if err != nil {
		c.record(ctx, EventConfigReloadRejected, map[string]interface{}{
			"section": policySectionKey,
			"reason":  err.Error(),
			"running": c.Profile().Name(),
		})
		return err
	}
	profile, err := Resolve(cfg)
	if err != nil {
		c.record(ctx, EventConfigReloadRejected, map[string]interface{}{
			"section": policySectionKey,
			"reason":  err.Error(),
			"running": c.Profile().Name(),
		})
		return err
	}
	batching := cfg.Batching
	c.profile.Store(profile)
	c.batching.Store(&batching)
	c.record(ctx, EventAutonomyProfileLoaded, map[string]interface{}{
		"profile":                 profile.Name(),
		"approval_batch_window_s": batching.WindowSeconds,
		"approval_batch_cap":      batching.Cap,
	})
	return nil
}

// record emits one audit event, allowed-fail. A nil emitter and a failing
// emitter are the same thing here: the decision path does not depend on
// the event having been written.
func (c *Controller) record(ctx context.Context, event string, fields map[string]interface{}) {
	if c.emit == nil {
		return
	}
	_ = c.emit(ctx, event, fields)
}
