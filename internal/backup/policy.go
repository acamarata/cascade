// Purpose: S-42.T1's multi-target backup policy (refs/chatgpt-2026-08-30
// §22 model): one TargetPolicy per target names its own cadence (a cron
// spec parsed through C/S-04.T4's own FuzzCronParse path -- this file adds
// no new parser) and domain set, plus the per-target snapshot Outcome
// bookkeeping every fire produces -- what schedule.go's fire closure writes
// and S-42.T4's future verification and S-42.T5's drill read.
//
// Inputs: a provider.Store namespace and TargetPolicy/Outcome values.
// Outputs: persisted records, sorted deterministically on every listing.
// Constraints: the §22 per-target VERIFIED state is S-42.T4's, not this
// file's -- Outcome records only "a snapshot fired for this target, at this
// time, with this result", never a verification claim.
//
// SPORT: internal.backup.policy/ADD (P1-E19-W4-S42-T1).

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
}

// policyKeyPrefix namespaces persisted TargetPolicy records, distinct from
// targets.go's "backup:target:" and this file's own "backup:outcome:".
const policyKeyPrefix = "backup:policy:"

func policyKey(target string) string { return policyKeyPrefix + target }

// Validate fails closed on a policy this build cannot safely schedule: an
// empty target name, an unparseable cron spec (via C/S-04.T4's own
// scheduler.ParseSpec -- no new parser), or an empty domain set.
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
	return nil
}

// policyWire is TargetPolicy's JSON wire shape.
type policyWire struct {
	Target   string   `json:"target"`
	CronSpec string   `json:"cron_spec"`
	Domains  []string `json:"domains"`
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

// Outcome is one fire's per-target bookkeeping: which target, which
// snapshot (empty when the fire never produced one -- e.g. the elevation
// refusal every unattended scheduled fire takes), when (from the injected
// Clock, never bare time.Now), and whether it succeeded. ErrorText carries
// the refusal/failure detail for a failed fire, empty on success.
type Outcome struct {
	Target    string
	Snapshot  string
	When      time.Time
	Success   bool
	ErrorText string
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
	Target    string `json:"target"`
	Snapshot  string `json:"snapshot,omitempty"`
	When      string `json:"when"`
	Success   bool   `json:"success"`
	ErrorText string `json:"error,omitempty"`
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
		})
	}
	if iterErr := it.Err(); iterErr != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, iterErr, "backup: scan target outcomes")
	}
	sort.Slice(outs, func(i, k int) bool { return outs[i].When.Before(outs[k].When) })
	return outs, nil
}
