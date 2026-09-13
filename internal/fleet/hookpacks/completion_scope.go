package hookpacks

// Purpose (this file): R-21.176's scope resolution -- the completion-gate
//
//	hook is installed into the user's OWN harness configuration, so it
//	fires for every session that harness runs, not only the ones Cascade
//	itself dispatched. This file decides, for one incoming
//	CompletionHookPayload, whether the event is IN SCOPE for the
//	completion gate at all (a Cascade-dispatched agent's session) before
//	completion_gate.go ever forms an opinion.
//
// Inputs: a CompletionHookPayload and a caller-supplied JobResolver.
// Outputs: ResolveJobID's (jobID, scoped, err) triple.
// Constraints: R-21.176 -- no job id resolves means scoped=false and the
//
//	caller must exit 0 with no opinion, never calling policy.completion_check;
//	a resolver error while scope itself cannot be determined (the daemon
//	is unreachable) is reported, not treated as a denial; only a
//	job-scoped payload whose presented id does not name a real job is a
//	fail-closed denial (KindNotFound). Real driver processes Cascade
//	dispatches inherit CASCADE_JOB_ID in their environment
//	(pkg/provider.driverEnvAllowlistBase) -- that is the payload field an
//	ordinary human session simply never carries, which is what makes an
//	empty JobID/TaskID the honest, common "unscoped" case rather than a
//	failure.
//
// SPORT: fleet/hookpacks.JobResolver/ResolveJobID/ADD (P1-E32-W6-S66-T1).

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// JobResolver maps a completion-hook payload's caller-presented job/task/
// session identity to a real, currently-known Cascade job id. The real
// implementation (cmd/cascade/hooks.go, at daemon startup) validates
// payload.JobID against the jobs domain's own Store -- there is no
// concrete type in this file, matching PolicyGate's own injection
// pattern.
//
// ResolveJob returns ("", nil) when nothing in payload identifies a job
// at all (the common, non-error "this is an ordinary human session"
// case). It returns a KindNotFound error when the payload names a job id
// that does not exist. Any other error (e.g. KindUnavailable) means
// scope itself could not be determined -- the resolver, or whatever it
// depends on, could not be reached.
type JobResolver interface {
	ResolveJob(ctx context.Context, payload CompletionHookPayload) (jobID string, err error)
}

// ResolveJobID implements R-21.176's scope decision. It never calls
// policy.completion_check itself -- that stays completion_gate.go's
// concern once scope is established.
func ResolveJobID(ctx context.Context, payload CompletionHookPayload, resolver JobResolver) (jobID string, scoped bool, err error) {
	if resolver == nil {
		return "", false, nil
	}
	id, rErr := resolver.ResolveJob(ctx, payload)
	if rErr != nil {
		if cascade.HasKind(rErr, cascade.KindNotFound) {
			// The payload DID name a job (that is the only way a real
			// JobResolver returns KindNotFound rather than ""), so this
			// is job-scoped -- the caller must fail closed. jobID is
			// still the caller-presented value so the deny reason and
			// journal entry can name what was rejected.
			return payload.JobID, true, rErr
		}
		// Scope itself could not be determined (the daemon or its store
		// is unreachable). This is NOT a denial -- R-21.176 requires
		// reporting it (cascade doctor --harness), never blocking an
		// ordinary human completion because the daemon happened to be
		// down or mid-upgrade.
		return "", false, rErr
	}
	if id == "" {
		return "", false, nil
	}
	return id, true, nil
}
