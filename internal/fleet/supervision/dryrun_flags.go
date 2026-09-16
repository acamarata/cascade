// Purpose: the durable first_run_done record for the dry-run-first gate —
//
//	one keyed record per {account, autonomy-profile} pair, answering
//	"has the first dry run happened" across daemon restarts.
//
// WHY NOT THE AUDIT LOG. R-14.247 §3: internal/audit exports an
//
//	append-only Writer with no read, so "has it happened" is
//	unanswerable through it. The flag is a keyed record in
//	pkg/provider.Store under the audit domain (storage.DomainAudit, the
//	same namespace internal/audit's Log writes through), following the
//	policy.NewStoreGrants / policy.NewStoreDenyList precedent. The dry
//	run's evaluation TRACE is the part that belongs in the append-only
//	log, and dryrun_enforce.go writes it there.
//
// Inputs: a provider.Store, an account id, and a profile version.
// Outputs: the flag's presence, or a typed refusal.
// Constraints: an absent key is NOT an error — it is the plain first-run
//
//	case. Any other read failure IS an error, and the caller must treat
//	it as UNKNOWN (R-14.247 §4): never as "the flag is set".
//
// SPORT: fleet.supervision.FirstRunFlags/ADDED (P1-E18-W4-S39-T4,
//
//	R-14.247). Split out of dryrun_enforce.go for the repo-wide
//	300-line cap, exactly as attention_store.go's header documents for
//	the identical reason.

package supervision

import (
	"context"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// FirstRunNamespace is the provider.Store namespace the flag persists
// under: the audit domain. Read from storage's own constant so the two can
// never drift.
const FirstRunNamespace = string(storage.DomainAudit)

// firstRunKeyPrefix namespaces the flags inside the audit domain. With the
// account and profile version it forms R-14.56's key:
// autoadvance:first_run_done:<account_id>:<profile_version>.
const firstRunKeyPrefix = "autoadvance:first_run_done:"

// firstRunDoneValue is the record's body. The KEY carries the pair; the
// value only has to exist, but it names itself so a store dump reads.
const firstRunDoneValue = `{"first_run_done":true}`

// FirstRunKey builds the flag key for one {account, profile-version} pair.
// The key is constructed, never parsed, so an account id containing a
// colon is unambiguous here.
func FirstRunKey(account, version string) string {
	return firstRunKeyPrefix + account + ":" + version
}

// FirstRunFlags answers whether the first dry run has completed, durably,
// so enforcement is strictly one-shot per {account, autonomy-profile}
// pair across daemon restarts.
type FirstRunFlags struct {
	store provider.Store
}

// NewFirstRunFlags builds the flag store. The store is required: a flag
// with nowhere to live could only answer by assuming, and both assumptions
// ("always set", "never set") break the guarantee in opposite ways.
func NewFirstRunFlags(store provider.Store) (*FirstRunFlags, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"supervision: first-run flags require a store")
	}
	return &FirstRunFlags{store: store}, nil
}

// Done reports whether the first dry run has completed for the pair.
//
// An absent key is false with NO error — that is the plain first-run case.
// Any other read failure returns the error: the caller MUST treat an error
// as UNKNOWN, never as "the flag is set" (R-14.247 §4).
func (f *FirstRunFlags) Done(ctx context.Context, account, version string) (bool, error) {
	if err := validatePair(account, version); err != nil {
		return false, err
	}
	_, err := f.store.Get(ctx, FirstRunNamespace, FirstRunKey(account, version))
	switch {
	case err == nil:
		return true, nil
	case cascade.HasKind(err, cascade.KindNotFound):
		return false, nil
	default:
		return false, err
	}
}

// Mark records that the first dry run has completed for the pair.
func (f *FirstRunFlags) Mark(ctx context.Context, account, version string) error {
	if err := validatePair(account, version); err != nil {
		return err
	}
	return f.store.Put(ctx, FirstRunNamespace, FirstRunKey(account, version), []byte(firstRunDoneValue))
}

// validatePair refuses a pair that cannot form a key. The account comes
// from a validated subject and the version from the running profile, so
// this is unreachable on the wired path; a caller that hands one in anyway
// gets a refusal rather than a flag every pair silently shares.
func validatePair(account, version string) error {
	if account == "" || version == "" {
		return cascade.New(cascade.KindInvalidInput,
			"supervision: first-run flags need an account and a profile version")
	}
	return nil
}
