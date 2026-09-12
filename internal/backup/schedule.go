// Purpose: S-42.T1's scheduler integration -- registering per-target backup
// jobs on C/S-04.T4's persisted cron scheduler, inheriting its semantics
// verbatim (job persistence across restart, advisory-lock exclusion,
// skip-missed), and I/S-18.T5's ActionRouter gating every dispatch already
// transits (scheduler.Scheduler.routeDispatch -- installed once via
// SetActionGate by the composition root, never by this file). S-42.T4
// extends this file with the parallel verification-job registration
// (RegisterVerificationJob/RegisterConfiguredVerificationJobs), riding the
// identical scheduler primitives, cadence source, and skip-unmatched
// reporting -- verification needs no elevation gate at all (it is
// read-only), so it carries none of the ElevationProof machinery below.
//
// Inputs: a *scheduler.Scheduler (already constructed, not yet Activated),
// a provider.Store namespace, and the TargetRecord/TargetPolicy pairs to
// register.
// Outputs: one persisted CronJob per target, registered before Activate.
// Constraints: NO ELEVATION BYPASS (06 §5.14/§5.24): the unattended,
// cron-triggered fire path always calls CreateSnapshot with an EMPTY
// ElevationProof -- there is no interactive attestation flow a background
// tick could supply one from, and no standing grant may cover an
// elevation-class verb. CreateSnapshot's own ErrElevationRequired refusal
// is therefore what "no snapshot, ever, from an unattended fire" means in
// this codebase: a structural guarantee (the closure below hardcodes ""),
// not a runtime check this file could accidentally skip.
//
// SPORT: internal.backup.schedule/ADD (P1-E19-W4-S42-T1); CHANGED (P1-E19-W4-S42-T4).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"context"

	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// backupJobOwnerPrefix names every scheduled backup Runnable this file
// registers, distinct from retention_register.go's "retention-*" owners
// and G/S-14.T3's memory-job owners sharing the same Scheduler.
const backupJobOwnerPrefix = "backup:target:"

func backupJobOwner(target string) string { return backupJobOwnerPrefix + target }

// verifyJobOwnerPrefix names every scheduled verification Runnable
// RegisterVerificationJob registers (S-42.T4), distinct from
// backupJobOwnerPrefix above so a target's create and verify jobs never
// collide as scheduler owners.
const verifyJobOwnerPrefix = "backup:verify:"

func verifyJobOwner(target string) string { return verifyJobOwnerPrefix + target }

// RegisterBackupJob registers one target's scheduled backup job on sched:
// a Runnable that fires fireBackupTarget with an empty ElevationProof (see
// this file's package doc), plus the persisted CronJob at policy.CronSpec.
// Call before sched.Activate, matching every other RegisterRunnable-based
// registration in this scheduler (retention_register.go,
// daemon_unix_memory_jobs.go's registerMemoryJobs). Re-registering the same
// target is idempotent, matching Scheduler.ScheduleJob's own upsert
// semantics.
func RegisterBackupJob(ctx context.Context, sched *scheduler.Scheduler, store provider.Store, namespace string, target TargetRecord, policy TargetPolicy, clock runtime.Clock, engine *egress.Engine, getenv func(string) string) error {
	if target.Name != policy.Target {
		return cascade.Newf(cascade.KindInvalidInput,
			"backup: policy target %q does not match target record %q", policy.Target, target.Name)
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	owner := backupJobOwner(target.Name)
	runnable := func(fireCtx context.Context) error {
		// The unattended, cron-triggered path: no interactive attestation
		// exists to supply, so proof is always empty and this fire can
		// never take a snapshot -- CreateSnapshot's own
		// ErrElevationRequired refusal enforces that, recorded as this
		// fire's failure Outcome.
		_, err := fireBackupTarget(fireCtx, store, namespace, "", target, engine, getenv, clock, "", nil, nil)
		return err
	}
	if err := sched.RegisterRunnable(owner, runnable); err != nil {
		return err
	}
	return sched.ScheduleJob(ctx, owner, policy.CronSpec, owner)
}

// RegisterConfiguredBackupJobs is the composition-root entry point: it
// lists every persisted TargetRecord/TargetPolicy pair in namespace and
// registers each as a scheduled backup job via RegisterBackupJob. A target
// with no matching policy (or vice versa) is skipped -- reported via the
// returned skipped slice, never silently -- since a target added before
// S-42.T3 also lands its policy verb cannot yet be scheduled. Safe to call
// on every daemon startup (idempotent, matching RegisterRetentionJobs'
// precedent); call before sched.Activate.
func RegisterConfiguredBackupJobs(ctx context.Context, sched *scheduler.Scheduler, store provider.Store, namespace string, clock runtime.Clock, engine *egress.Engine, getenv func(string) string) (skipped []string, err error) {
	targetsList, err := ListTargets(ctx, store, namespace)
	if err != nil {
		return nil, err
	}
	policies, err := ListPolicies(ctx, store, namespace)
	if err != nil {
		return nil, err
	}
	byTarget := make(map[string]TargetPolicy, len(policies))
	for _, p := range policies {
		byTarget[p.Target] = p
	}
	for _, target := range targetsList {
		policy, ok := byTarget[target.Name]
		if !ok {
			skipped = append(skipped, target.Name)
			continue
		}
		if err := RegisterBackupJob(ctx, sched, store, namespace, target, policy, clock, engine, getenv); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

// fireBackupTarget builds target's real S-41.T3 driver (BuildTarget) and
// runs S-41.T2's CreateSnapshot with proof over domains, then records the
// resulting Outcome regardless of success or failure -- the per-target
// bookkeeping S-42.T4's verification and S-42.T5's drill read. domains may
// be nil when proof is empty: CreateSnapshot's elevation check is its very
// first statement (snapshot.go), executed before deps.Domains is ever
// read, so the unattended path -- which never has a proof -- needs no live
// Exporter resolution to prove it takes no snapshot.
func fireBackupTarget(ctx context.Context, store provider.Store, namespace string, proof ElevationProof, target TargetRecord, engine *egress.Engine, getenv func(string) string, clock runtime.Clock, ageRecipient string, domains map[string]Exporter, previous *Manifest) (Manifest, error) {
	when := clock.Now()
	driver, err := BuildTarget(ctx, target, engine, getenv)
	if err != nil {
		_ = RecordOutcome(ctx, store, namespace, Outcome{Target: target.Name, When: when, ErrorText: err.Error()})
		return Manifest{}, err
	}
	m, snapErr := CreateSnapshot(ctx, proof, CreateSnapshotDeps{
		Target: driver, AgeRecipient: ageRecipient, Clock: clock, Domains: domains,
	}, previous)
	outcome := Outcome{Target: target.Name, When: when, Success: snapErr == nil}
	if snapErr != nil {
		outcome.ErrorText = snapErr.Error()
	} else {
		outcome.Snapshot = string(m.Snapshot)
	}
	if recErr := RecordOutcome(ctx, store, namespace, outcome); recErr != nil && snapErr == nil {
		return m, recErr
	}
	return m, snapErr
}

// RegisterVerificationJob registers one target's scheduled verification
// job (S-42.T4) on sched: a Runnable that calls RunVerification, plus the
// persisted CronJob at policy.EffectiveVerifyCronSpec(). A policy with
// VerifyDisabled set registers nothing for this target and returns nil --
// the per-target toggle, read as policy-record data (no config.toml key).
// Call before sched.Activate, matching RegisterBackupJob's own precedent.
func RegisterVerificationJob(ctx context.Context, sched *scheduler.Scheduler, store provider.Store, namespace string, target TargetRecord, policy TargetPolicy, clock runtime.Clock, engine *egress.Engine, getenv func(string) string, sink AttentionSink) error {
	if target.Name != policy.Target {
		return cascade.Newf(cascade.KindInvalidInput,
			"backup: policy target %q does not match target record %q", policy.Target, target.Name)
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.VerifyDisabled {
		return nil
	}
	owner := verifyJobOwner(target.Name)
	runnable := func(fireCtx context.Context) error {
		_, err := RunVerification(fireCtx, store, namespace, target, engine, getenv, clock, sink)
		return err
	}
	if err := sched.RegisterRunnable(owner, runnable); err != nil {
		return err
	}
	return sched.ScheduleJob(ctx, owner, policy.EffectiveVerifyCronSpec(), owner)
}

// RegisterConfiguredVerificationJobs is the composition-root entry point
// (S-42.T4), mirroring RegisterConfiguredBackupJobs exactly: it lists
// every persisted TargetRecord/TargetPolicy pair in namespace and
// registers each as a scheduled verification job via
// RegisterVerificationJob. A target with no matching policy is skipped --
// reported via the returned skipped slice, never silently. Safe to call
// on every daemon startup; call before sched.Activate.
func RegisterConfiguredVerificationJobs(ctx context.Context, sched *scheduler.Scheduler, store provider.Store, namespace string, clock runtime.Clock, engine *egress.Engine, getenv func(string) string, sink AttentionSink) (skipped []string, err error) {
	targetsList, err := ListTargets(ctx, store, namespace)
	if err != nil {
		return nil, err
	}
	policies, err := ListPolicies(ctx, store, namespace)
	if err != nil {
		return nil, err
	}
	byTarget := make(map[string]TargetPolicy, len(policies))
	for _, p := range policies {
		byTarget[p.Target] = p
	}
	for _, target := range targetsList {
		policy, ok := byTarget[target.Name]
		if !ok {
			skipped = append(skipped, target.Name)
			continue
		}
		if err := RegisterVerificationJob(ctx, sched, store, namespace, target, policy, clock, engine, getenv, sink); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}
