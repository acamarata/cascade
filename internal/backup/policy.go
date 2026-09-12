// Purpose: S-42.T1's multi-target backup policy (refs/chatgpt-2026-08-30
// §22 model): one TargetPolicy per target names its own cadence (a cron
// spec parsed through C/S-04.T4's own FuzzCronParse path -- this file adds
// no new parser) and domain set, plus the per-target snapshot Outcome
// bookkeeping every fire produces -- what schedule.go's fire closure writes
// and S-42.T4's verification and S-42.T5's drill read.
//
// S-42.T4 EXTENSION: VerifyCronSpec/VerifyDisabled are this ticket's own
// per-target verification cadence/toggle -- policy-record DATA (08 §3 has
// no [backup] config.toml section), never a second parser: VerifyCronSpec
// rides the identical scheduler.ParseSpec path CronSpec already uses. An
// empty VerifyCronSpec means "use DefaultVerifyCronSpec" (the ticket's 24h
// default), so every TargetPolicy record S-42.T1 already persisted decodes
// with the correct default with no migration. Outcome gained Kind/
// CheckedChunks/DurationMS (all omitempty on the wire) so a verification
// fire's record is distinguishable from a create fire's without breaking
// any already-persisted create Outcome (Kind decodes to "" -> OutcomeKindCreate).
//
// Inputs: a provider.Store namespace and TargetPolicy/Outcome values.
// Outputs: persisted records, sorted deterministically on every listing.
// Constraints: the §22 per-target VERIFIED state is recorded by an Outcome
// with Kind == OutcomeKindVerify and Success == true -- Outcome records
// otherwise only say "a fire happened for this target, at this time, with
// this result".
//
// SPORT: internal.backup.policy/ADD (P1-E19-W4-S42-T1); CHANGED (P1-E19-W4-S42-T4).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TargetPolicy is the §22 per-target cadence policy: which target it
// governs, its own cron spec (e.g. NAS every 6 hours, cloud daily), and the
// domain set a due fire captures for that target. Target must name an
// existing TargetRecord (checked by RegisterBackupJob, not here -- this
// type only encodes the policy's own shape).
type TargetPolicy struct {
	Target   string
	CronSpec string
	Domains  []string
	// VerifyCronSpec is this target's verification cadence (S-42.T4). Empty
	// means DefaultVerifyCronSpec ("@every 24h").
	VerifyCronSpec string
	// VerifyDisabled turns OFF scheduled verification for this target when
	// true (S-42.T4's per-target toggle). The zero value (false) keeps
	// verification ON by default -- an already-persisted S-42.T1 policy
	// record predating this field decodes as VerifyDisabled == false.
	VerifyDisabled bool
}

// DefaultVerifyCronSpec is the S-42.T4 policy-record default cadence when
// a TargetPolicy leaves VerifyCronSpec empty. This is DATA, never a
// config.toml key (08 §3 has no [backup] section).
const DefaultVerifyCronSpec = "@every 24h"

// EffectiveVerifyCronSpec returns p.VerifyCronSpec, or DefaultVerifyCronSpec
// when p leaves it unset.
func (p TargetPolicy) EffectiveVerifyCronSpec() string {
	if strings.TrimSpace(p.VerifyCronSpec) == "" {
		return DefaultVerifyCronSpec
	}
	return p.VerifyCronSpec
}

// policyKeyPrefix namespaces persisted TargetPolicy records, distinct from
// targets.go's "backup:target:" and this file's own "backup:outcome:".
const policyKeyPrefix = "backup:policy:"

func policyKey(target string) string { return policyKeyPrefix + target }

// Validate fails closed on a policy this build cannot safely schedule: an
// empty target name, an unparseable cron spec (via C/S-04.T4's own
// scheduler.ParseSpec -- no new parser), an empty domain set, or (S-42.T4)
// a non-empty VerifyCronSpec that fails the same parser.
func (p TargetPolicy) Validate() error {
	if strings.TrimSpace(p.Target) == "" {
		return cascade.New(cascade.KindInvalidInput, "backup: policy target name is required")
	}
	if _, err := scheduler.ParseSpec(p.CronSpec); err != nil {
		return err
	}
	if len(p.Domains) == 0 {
		return cascade.New(cascade.KindInvalidInput, "backup: policy requires at least one domain")
	}
	if strings.TrimSpace(p.VerifyCronSpec) != "" {
		if _, err := scheduler.ParseSpec(p.VerifyCronSpec); err != nil {
			return err
		}
	}
	return nil
}

// policyWire is TargetPolicy's JSON wire shape. Field order/types must
// match TargetPolicy exactly: encodePolicy/decodePolicy convert between
// the two directly (policyWire(p) / TargetPolicy(wire)), not field by
// field.
type policyWire struct {
	Target         string   `json:"target"`
	CronSpec       string   `json:"cron_spec"`
	Domains        []string `json:"domains"`
	VerifyCronSpec string   `json:"verify_cron_spec,omitempty"`
	VerifyDisabled bool     `json:"verify_disabled,omitempty"`
}

func encodePolicy(p TargetPolicy) ([]byte, error) {
	data, err := json.Marshal(policyWire(p))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: encode target policy")
	}
	return data, nil
}

func decodePolicy(data []byte) (TargetPolicy, error) {
	var wire policyWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return TargetPolicy{}, cascade.Wrap(cascade.KindIntegrity, err, "backup: corrupt target policy")
	}
	return TargetPolicy(wire), nil
}

// PutPolicy upserts p's record in namespace, after Validate.
func PutPolicy(ctx context.Context, store provider.Store, namespace string, p TargetPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	data, err := encodePolicy(p)
	if err != nil {
		return err
	}
	if err := store.Put(ctx, namespace, policyKey(p.Target), data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: persist target policy")
	}
	return nil
}

// GetPolicy reads one persisted TargetPolicy by target name, or a
// KindNotFound error if absent.
func GetPolicy(ctx context.Context, store provider.Store, namespace, target string) (TargetPolicy, error) {
	data, err := store.Get(ctx, namespace, policyKey(target))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return TargetPolicy{}, err
		}
		return TargetPolicy{}, cascade.Wrap(cascade.KindUnavailable, err, "backup: read target policy")
	}
	return decodePolicy(data)
}

// ListPolicies returns every persisted TargetPolicy in namespace, sorted by
// target name.
func ListPolicies(ctx context.Context, store provider.Store, namespace string) ([]TargetPolicy, error) {
	it, err := store.Scan(ctx, namespace, policyKeyPrefix)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "backup: scan target policies")
	}
	defer func() { _ = it.Close() }()

	var pols []TargetPolicy
	for it.Next(ctx) {
		p, derr := decodePolicy(it.Value())
		if derr != nil {
			return nil, derr
		}
		pols = append(pols, p)
	}
	if iterErr := it.Err(); iterErr != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, iterErr, "backup: scan target policies")
	}
	sort.Slice(pols, func(i, k int) bool { return pols[i].Target < pols[k].Target })
	return pols, nil
}

// OutcomeKindCreate/OutcomeKindVerify/OutcomeEffectiveKind (S-42.T4) live
// in verify.go, next to their one real producer/consumer, to keep this
// file under the 300-line cap.

// Outcome is one fire's per-target bookkeeping: which target, which
// snapshot (empty when the fire never produced one -- e.g. the elevation
// refusal every unattended scheduled fire takes), when (from the injected
// Clock, never bare time.Now), and whether it succeeded. ErrorText carries
// the refusal/failure detail for a failed fire, empty on success.
//
// Kind/CheckedChunks/DurationMS (S-42.T4) are populated for a verification
// fire only (Kind == OutcomeKindVerify); a create fire leaves them zero.
// A Success verification Outcome IS the §22 per-target VERIFIED state --
// there is no separate boolean or record shape for it.
type Outcome struct {
	Target    string
	Snapshot  string
	When      time.Time
	Success   bool
	ErrorText string
	// Kind is OutcomeKindCreate or OutcomeKindVerify. Empty decodes to
	// OutcomeKindCreate (see OutcomeEffectiveKind) for wire compatibility
	// with every Outcome S-42.T1 already persisted.
	Kind string
	// CheckedChunks is VerifyIntegrity's GateReport.ObjectsVerified for a
	// successful verification fire; zero otherwise.
	CheckedChunks int
	// DurationMS is the verification pass's wall-clock duration in
	// milliseconds, measured via the injected Clock (never bare
	// time.Now); zero for a create fire.
	DurationMS int64
}

// outcomeKeyPrefix namespaces persisted Outcome records, one per target per
// fire, ordered lexicographically by (target, RFC3339Nano when, so the same
// target's outcomes sort chronologically under its own sub-prefix.
const outcomeKeyPrefix = "backup:outcome:"

func outcomeKey(target string, when time.Time) string {
	return outcomeKeyPrefix + target + ":" + when.UTC().Format(time.RFC3339Nano)
}

// outcomeWire is Outcome's JSON wire shape.
type outcomeWire struct {
	Target        string `json:"target"`
	Snapshot      string `json:"snapshot,omitempty"`
	When          string `json:"when"`
	Success       bool   `json:"success"`
	ErrorText     string `json:"error,omitempty"`
	Kind          string `json:"kind,omitempty"`
	CheckedChunks int    `json:"checked_chunks,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
}

// RecordOutcome persists one fire's Outcome. Outcomes are append-only (each
// key carries its own timestamp) -- a fire never overwrites a prior one's
// record.
func RecordOutcome(ctx context.Context, store provider.Store, namespace string, o Outcome) error {
	if strings.TrimSpace(o.Target) == "" {
		return cascade.New(cascade.KindInvalidInput, "backup: outcome target name is required")
	}
	if o.When.IsZero() {
		return cascade.New(cascade.KindInvalidInput, "backup: outcome requires a non-zero When")
	}
	wire := outcomeWire{
		Target: o.Target, Snapshot: o.Snapshot,
		When: o.When.UTC().Format(time.RFC3339Nano), Success: o.Success, ErrorText: o.ErrorText,
		Kind: o.Kind, CheckedChunks: o.CheckedChunks, DurationMS: o.DurationMS,
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode target outcome")
	}
	if err := store.Put(ctx, namespace, outcomeKey(o.Target, o.When), data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: persist target outcome")
	}
	return nil
}

// ListOutcomes returns every persisted Outcome for target, sorted
// chronologically (oldest first) by When.
func ListOutcomes(ctx context.Context, store provider.Store, namespace, target string) ([]Outcome, error) {
	it, err := store.Scan(ctx, namespace, outcomeKeyPrefix+target+":")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "backup: scan target outcomes")
	}
	defer func() { _ = it.Close() }()

	var outs []Outcome
	for it.Next(ctx) {
		var wire outcomeWire
		if err := json.Unmarshal(it.Value(), &wire); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: corrupt target outcome")
		}
		when, err := time.Parse(time.RFC3339Nano, wire.When)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: corrupt target outcome timestamp")
		}
		outs = append(outs, Outcome{
			Target: wire.Target, Snapshot: wire.Snapshot, When: when,
			Success: wire.Success, ErrorText: wire.ErrorText,
			Kind: wire.Kind, CheckedChunks: wire.CheckedChunks, DurationMS: wire.DurationMS,
		})
	}
	if iterErr := it.Err(); iterErr != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, iterErr, "backup: scan target outcomes")
	}
	sort.Slice(outs, func(i, k int) bool { return outs[i].When.Before(outs[k].When) })
	return outs, nil
}
