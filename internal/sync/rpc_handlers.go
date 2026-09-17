package sync

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the three handler bodies, split from rpc.go's
//
//	registration under the 300-line cap. The cut is at registration versus
//	behaviour, which is the seam a reader would draw anyway.
//
// SPORT: internal/sync rpc handlers (ADD) — P1-E17-W4-S38-T3.

// Status joins what each domain IS with what this peer may do with it.
//
// Every registered domain appears, including the ones this peer may not
// sync. A status that listed only the eligible ones would answer "why is
// memory not syncing?" by omitting memory, which is the least useful
// possible answer.
func (d Deps) Status(ctx context.Context) (StatusResult, error) {
	if err := d.requireEngine(); err != nil {
		return StatusResult{}, err
	}
	classes := AllCoreClasses()
	out := StatusResult{PeerTier: string(d.PeerTier), Domains: make([]DomainStatus, 0, len(classes))}
	for _, dc := range classes {
		strategy, _ := StrategyFor(dc.Domain, dc.Subkind)
		class := d.classOf(dc)
		line := DomainStatus{
			Domain: string(dc.Domain), Subkind: dc.Subkind,
			Class: string(class), Strategy: string(strategy),
		}
		verdict := d.eligibility(dc)
		line.Eligible, line.Reason = verdict.Eligible, verdict.Reason
		line.Position, line.PositionKnown = d.positionOf(ctx, dc)
		out.Domains = append(out.Domains, line)
	}
	sort.Slice(out.Domains, func(a, b int) bool {
		if out.Domains[a].Domain != out.Domains[b].Domain {
			return out.Domains[a].Domain < out.Domains[b].Domain
		}
		return out.Domains[a].Subkind < out.Domains[b].Subkind
	})
	out.OpenConflicts = d.Engine.Conflicts().Len()
	return out, nil
}

// positionOf reads one domain's cursor, reporting whether it could.
//
// The bool exists because zero is a real position — a domain that has
// never synced is AT zero — and "I could not read it" is a different fact.
// Collapsing them prints a number an operator would believe. A read
// failure is not fatal to the whole report, though: a status that refused
// because one domain's cursor was unreadable would be useless exactly
// when somebody most wants it.
func (d Deps) positionOf(ctx context.Context, dc DomainClass) (uint64, bool) {
	if d.Engine == nil {
		return 0, false
	}
	cur, err := d.Engine.cursors.Get(ctx, dc.Domain, dc.Subkind)
	if err != nil {
		return 0, false
	}
	return cur.Position, true
}

// RunDomains syncs one domain, or every eligible one.
//
// An unknown --domain is an ERROR naming what is registered, not a no-op.
// A caller who typed a domain that does not exist asked for something, and
// reporting success for having done nothing is the shape of a bug they
// will not find for weeks.
func (d Deps) RunDomains(ctx context.Context, domain string) (RunResult, error) {
	return d.run(ctx, runParams{Domain: domain, Subkind: domain})
}

// run is RunDomains' body, taking the decoded params shape the RPC layer
// produces so the CLI and the RPC reach identical code.
func (d Deps) run(ctx context.Context, p runParams) (RunResult, error) {
	// THE REQUEST IS VALIDATED BEFORE THE CAPABILITY IS REPORTED. A
	// caller who typed `--domain confgi` has a typo whether or not a run
	// path is wired, and answering them with "nothing is wired" hides the
	// mistake behind a fact about this machine. Checking the other way
	// round also made the unknown-domain refusal unreachable from the
	// CLI, which is exactly how an error path stops being tested.
	targets, err := d.targets(p)
	if err != nil {
		return RunResult{}, err
	}
	if d.Run == nil {
		return RunResult{}, cascade.New(cascade.KindUnavailable,
			"sync: no run path is wired, so nothing would be synced; "+
				"reporting success here would be a lie an operator could not see through")
	}
	out := RunResult{Domains: make([]RunDomain, 0, len(targets))}
	for _, dc := range targets {
		line := RunDomain{Domain: string(dc.Domain), Subkind: dc.Subkind}
		if err := d.Run(ctx, dc.Domain, dc.Subkind); err != nil {
			// One domain's failure does not stop the others: a sync that
			// abandoned four healthy domains because the fifth's remote
			// was down would make an operator's whole fleet wait on one
			// machine.
			line.Error = err.Error()
		} else {
			line.Synced = true
		}
		out.Domains = append(out.Domains, line)
	}
	return out, nil
}

// targets resolves which domains one run covers.
func (d Deps) targets(p runParams) ([]DomainClass, error) {
	if p.Domain == "" {
		var eligible []DomainClass
		for _, dc := range AllCoreClasses() {
			if d.eligibility(dc).Eligible {
				eligible = append(eligible, dc)
			}
		}
		return eligible, nil
	}
	subkind := p.Subkind
	if subkind == "" {
		subkind = p.Domain
	}
	dc, ok := Lookup(storage.DomainID(p.Domain), subkind)
	if !ok {
		return nil, errUnknownSyncDomain(p.Domain, subkind)
	}
	if verdict := d.eligibility(dc); !verdict.Eligible {
		// The refusal names what this tier CAN sync. "You may not sync
		// this" leaves an operator guessing at the shape of the rule;
		// the list is the diagnostic half of the domain x tier table.
		return nil, cascade.Wrapf(cascade.KindPolicyDenied,
			errTierMaySyncNothingHere(dc.Domain, dc.Subkind, d.PeerTier),
			"sync: %s/%s does not sync here (%s); this tier may sync %v",
			dc.Domain, dc.Subkind, verdict.Reason, EligibleDomains(d.PeerTier))
	}
	return []DomainClass{dc}, nil
}

// classOf resolves dc's effective sync class, after the operator's
// `[sync]` overrides. Overrides only ever narrow (config.go refuses a
// widening reload), so this can turn a syncing domain local-only and
// never the other way round.
func (d Deps) classOf(dc DomainClass) Class {
	return d.Config.Resolve(string(dc.Domain)+"/"+dc.Subkind, dc.Class)
}

// eligibility answers the same question for status and for run, over the
// EFFECTIVE class.
//
// One function rather than two call sites, because the failure mode of
// two is a status that says a domain syncs and a run that quietly skips
// it — and an operator reading the report would have no way to tell.
func (d Deps) eligibility(dc DomainClass) EligibilityVerdict {
	if d.classOf(dc) == ClassLocalOnly {
		return EligibilityVerdict{Reason: "domain-local-only"}
	}
	return Eligible(Record{Domain: dc.Domain, Subkind: dc.Subkind, Tier: dc.Sensitivity}, d.PeerTier)
}

// RunResult is sync.run's answer.
type RunResult struct {
	Domains []RunDomain `json:"domains"`
}

// RunDomain is one domain's outcome in a run.
type RunDomain struct {
	Domain  string `json:"domain"`
	Subkind string `json:"subkind"`
	Synced  bool   `json:"synced"`
	// Error is the failure text when this domain did not sync. Reported
	// per domain rather than as a whole-run failure, because the other
	// domains really did sync and an operator needs to know which.
	Error string `json:"error,omitempty"`
}

// Resolve settles one journaled conflict.
//
// Keeping the SERVER's side is not elevated: it is what the merge already
// decided, and re-affirming it changes nothing. Keeping the LOCAL side
// discards the server's copy, which overrides the authority a
// server-primary domain is defined by — so it goes through the gate, and
// a missing gate refuses.
func (d Deps) Resolve(ctx context.Context, recordID, keep string) (ResolveResult, error) {
	return d.resolve(ctx, resolveParams{RecordID: recordID, Keep: keep})
}

// resolve is Resolve's body, over the decoded params the RPC produces.
func (d Deps) resolve(ctx context.Context, p resolveParams) (ResolveResult, error) {
	if err := d.requireEngine(); err != nil {
		return ResolveResult{}, err
	}
	if p.RecordID == "" {
		return ResolveResult{}, cascade.New(cascade.KindInvalidInput,
			"sync: resolving a conflict needs the record id it is about")
	}
	// THE REQUEST IS VALIDATED BEFORE THE STATE IS CONSULTED, as in run:
	// a --keep this verb does not know is a malformed request whatever
	// the journal holds, and answering it with "no such conflict" sends
	// the caller looking for the wrong mistake. It also kept the
	// invalid-side refusal unreachable for every id the journal did not
	// carry, which is most of them.
	if p.Keep != KeepServer && p.Keep != KeepLocal {
		return ResolveResult{}, cascade.Newf(cascade.KindInvalidInput,
			"sync: %q is not a side to keep; it is %q or %q, and the choice is a parameter "+
				"rather than a question, so a non-interactive run can make it", p.Keep, KeepServer, KeepLocal)
	}
	if !d.knownConflict(p.RecordID) {
		return ResolveResult{}, errUnknownConflict(p.RecordID)
	}
	switch p.Keep {
	case KeepServer:
		return ResolveResult{RecordID: p.RecordID, Keep: KeepServer}, nil
	default:
		// KeepLocal: the switch above admitted nothing else.
		if d.Gate == nil {
			return ResolveResult{}, cascade.Newf(cascade.KindElevationRequired,
				"sync: discarding the server's copy of %q needs authorization, and no elevation gate "+
					"is wired; a machine that cannot check must not be the one that allows it", p.RecordID)
		}
		if err := d.Gate.Authorize(ctx, ElevatedVerbResolve); err != nil {
			return ResolveResult{}, err
		}
		return ResolveResult{RecordID: p.RecordID, Keep: KeepLocal, Elevated: true}, nil
	}
}

// requireEngine refuses every verb that would read the journal when no
// engine was composed.
//
// A refusal rather than an empty answer: "there are no conflicts" and
// "nothing here can tell you whether there are conflicts" are different
// facts, and only one of them means an operator can stop looking. Before
// this existed the nil engine reached ConflictJournal.Len() and panicked,
// which at least was not quiet.
func (d Deps) requireEngine() error {
	if d.Engine == nil {
		return cascade.New(cascade.KindUnavailable,
			"sync: no engine is composed, so nothing here can answer for the conflict journal")
	}
	return nil
}

// knownConflict reports whether the journal carries recordID.
func (d Deps) knownConflict(recordID string) bool {
	for _, c := range d.Engine.Conflicts().List() {
		if c.RecordID == recordID {
			return true
		}
	}
	return false
}

// errUnknownSyncDomain refuses a domain nobody registered, naming what is
// registered so the caller can see what they meant.
func errUnknownSyncDomain(domain, subkind string) error {
	var known []string
	for _, dc := range AllCoreClasses() {
		known = append(known, string(dc.Domain)+"/"+dc.Subkind)
	}
	sort.Strings(known)
	return cascade.Newf(cascade.KindNotFound,
		"sync: %s/%s is not a registered sync domain; the registered ones are %v", domain, subkind, known)
}

// errUnknownConflict refuses a resolution for a conflict the journal does
// not carry.
func errUnknownConflict(recordID string) error {
	return cascade.Newf(cascade.KindNotFound,
		"sync: no journaled conflict names record %q; `sync conflicts list` shows the open ones", recordID)
}
