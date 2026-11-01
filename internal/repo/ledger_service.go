package repo

// Purpose: LedgerService, the one writer of the inferred-fact ledger above
//   LedgerStore. It versions every proposal, corroborates it against the live
//   inventory (Corroborate), accepts, rejects or keeps it proposed, records an
//   operator's elevated accept, and exposes accepted facts as the read API
//   (AcceptedFacts) every consumer reads (P2-PEW-10, P1-GEN-06).
// Inputs: an inventory *Store and a *LedgerStore over one migrated database.
// Outputs: InferredFact rows, ProposeResult and AcceptedFact values, or typed
//   cascade errors.
// Constraints: AcceptedBy is set only here (DetectorAcceptedBy or
//   AcceptedByOperator), never from a caller value; Source is always
//   provider.ProvenanceUntrustedSource; rows are appended, never overwritten,
//   so every version stays queryable; AcceptedFacts passes each fact through
//   ProjectPolicy on its own and drops a refused one into denied.
// SPORT: repo/inferred-fact-ledger/ADD (P1-GEN-02).

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// AcceptedByOperator is the AcceptedBy value of an operator's elevated accept.
const AcceptedByOperator = "operator:elevated"

// DetectorAcceptedBy names the detector family that corroborates s:
// "detector:languages", "detector:commands" or "detector:ci". A subject no
// detector corroborates yields "".
func DetectorAcceptedBy(s FactSubject) string {
	switch s {
	case SubjectLanguages:
		return "detector:languages"
	case SubjectBuildCmd, SubjectTestCmd, SubjectLintCmd:
		return "detector:commands"
	case SubjectCIPresence:
		return "detector:ci"
	case SubjectLayout, SubjectHarnessFiles:
		return "" // no detector corroborates these
	}
	return ""
}

// FactTruth is how an accepted fact earned its acceptance.
type FactTruth string

const (
	// TruthDetectorCorroborated marks a fact a deterministic detector matched.
	TruthDetectorCorroborated FactTruth = "detector-corroborated"
	// TruthOperatorAccepted marks a fact an operator accepted under elevation.
	TruthOperatorAccepted FactTruth = "operator-accepted"
)

// AcceptedFact is one row of the read API: an accepted, projectable fact.
type AcceptedFact struct {
	ID         string
	Version    int
	Subject    FactSubject
	Fact       string
	Truth      FactTruth
	Provenance provider.Provenance
	AcceptedBy string
}

// ProposeResult splits the rows one Record call appended by outcome.
type ProposeResult struct {
	Proposed, Accepted, Rejected []InferredFact
}

// LedgerService versions, corroborates and serves inferred facts.
type LedgerService struct {
	inv    *Store
	ledger *LedgerStore
}

// NewLedgerService builds the service. A nil store is KindInvalidInput.
func NewLedgerService(inv *Store, ledger *LedgerStore) (*LedgerService, error) {
	if inv == nil || ledger == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: NewLedgerService requires a non-nil inventory and ledger store")
	}
	return &LedgerService{inv: inv, ledger: ledger}, nil
}

// Record appends one row per proposal: ID = FactID, Version = max stored + 1,
// Source untrusted-source. A corroborated row is accepted by its detector and
// supersedes the prior accepted version; a contradicted (or unparseable) one
// is rejected with the reason; any other stays proposed. Every proposal is
// validated before the first append, so a bad one appends nothing.
func (s *LedgerService) Record(ctx context.Context, repoID string, ps []ProposedFact) (ProposeResult, error) {
	var res ProposeResult
	rows, err := s.List(ctx, repoID)
	if err != nil {
		return res, err
	}
	next := nextVersions(rows)
	facts := make([]InferredFact, len(ps))
	for i, p := range ps {
		id := FactID(repoID, p.Subject)
		facts[i] = InferredFact{
			ID: id, RepositoryID: repoID, Subject: p.Subject, Fact: p.Fact,
			Source: string(provider.ProvenanceUntrustedSource), Confidence: p.Confidence,
			Version: next[id] + 1, State: FactProposed,
		}
		next[id]++
		if err := facts[i].Validate(); err != nil {
			return ProposeResult{}, err
		}
	}
	for _, f := range facts {
		out, err := s.recordOne(ctx, f)
		if err != nil {
			return res, err
		}
		switch out.State {
		case FactAccepted:
			res.Accepted = append(res.Accepted, out)
		case FactRejected:
			res.Rejected = append(res.Rejected, out)
		case FactProposed, FactSuperseded:
			fallthrough
		default:
			res.Proposed = append(res.Proposed, out)
		}
	}
	return res, nil
}

// recordOne corroborates f and appends the resulting row.
func (s *LedgerService) recordOne(ctx context.Context, f InferredFact) (InferredFact, error) {
	verdict, reason, err := Corroborate(ctx, s.inv, f)
	if err != nil {
		if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
			return InferredFact{}, err
		}
		verdict, reason = VerdictContradicted, "schema: "+err.Error()
	}
	switch verdict {
	case VerdictCorroborated:
		return s.acceptNew(ctx, f, DetectorAcceptedBy(f.Subject))
	case VerdictContradicted:
		rejected, err := Reject(f, reason)
		if err != nil {
			return InferredFact{}, err
		}
		return rejected, s.ledger.Append(ctx, rejected)
	case VerdictUncorroborated:
		// no detector decided: the proposal is recorded as it came
	}
	return f, s.ledger.Append(ctx, f)
}

// acceptNew appends f accepted by acceptedBy, pointing Rollback at the prior
// accepted version, then marks that prior version superseded.
func (s *LedgerService) acceptNew(ctx context.Context, f InferredFact, acceptedBy string) (InferredFact, error) {
	accepted, err := Accept(f, acceptedBy)
	if err != nil {
		return InferredFact{}, err
	}
	rows, err := s.ledger.List(ctx, LedgerFilter{RepositoryID: f.RepositoryID, Subject: f.Subject, State: FactAccepted})
	if err != nil {
		return InferredFact{}, err
	}
	var priors []InferredFact
	for _, r := range rows {
		if r.ID == f.ID && r.Version < accepted.Version {
			priors = append(priors, r)
			accepted.Rollback = rowKeyFor(r)
		}
	}
	if err := s.ledger.Append(ctx, accepted); err != nil {
		return InferredFact{}, err
	}
	for _, p := range priors {
		p.State = FactSuperseded
		if err := s.ledger.UpdateState(ctx, p); err != nil {
			return InferredFact{}, err
		}
	}
	return accepted, nil
}

// List returns every stored version of every fact for repoID.
func (s *LedgerService) List(ctx context.Context, repoID string) ([]InferredFact, error) {
	return s.ledger.List(ctx, LedgerFilter{RepositoryID: repoID})
}

// AcceptByOperator accepts the newest proposed version of factID as a new
// version (max stored + 1) with AcceptedByOperator, superseding the proposed
// row it copies and the prior accepted version. No proposed version is
// KindNotFound.
func (s *LedgerService) AcceptByOperator(ctx context.Context, factID string) (InferredFact, error) {
	repoID, subject, err := splitFactID(factID)
	if err != nil {
		return InferredFact{}, err
	}
	rows, err := s.ledger.List(ctx, LedgerFilter{RepositoryID: repoID, Subject: subject})
	if err != nil {
		return InferredFact{}, err
	}
	var newest *InferredFact
	maxVersion := 0
	for i := range rows {
		if rows[i].ID != factID {
			continue
		}
		maxVersion = max(maxVersion, rows[i].Version)
		if rows[i].State == FactProposed {
			newest = &rows[i]
		}
	}
	if newest == nil {
		return InferredFact{}, cascade.Newf(cascade.KindNotFound, "repo: fact %s has no proposed version to accept", factID)
	}
	copied := *newest
	copied.Version = maxVersion + 1
	accepted, err := s.acceptNew(ctx, copied, AcceptedByOperator)
	if err != nil {
		return InferredFact{}, err
	}
	newest.State = FactSuperseded
	return accepted, s.ledger.UpdateState(ctx, *newest)
}

// splitFactID parses "fact:<repositoryID>:<subject>".
func splitFactID(factID string) (string, FactSubject, error) {
	rest, ok := strings.CutPrefix(factID, "fact:")
	i := strings.LastIndex(rest, ":")
	if !ok || i <= 0 {
		return "", "", cascade.Newf(cascade.KindInvalidInput, "repo: %q is not a fact id", factID)
	}
	subject := FactSubject(rest[i+1:])
	if !subject.Valid() {
		return "", "", cascade.Newf(cascade.KindInvalidInput, "repo: %q names no closed fact subject", factID)
	}
	return rest[:i], subject, nil
}

// AcceptedFacts is the read API: the newest accepted version of each fact,
// each passed through ProjectPolicy on its own. A fact the projection refuses
// (KindPolicyDenied) goes into denied by id, never into facts.
func (s *LedgerService) AcceptedFacts(ctx context.Context, repoID string) (facts []AcceptedFact, denied []string, err error) {
	rows, err := s.ledger.List(ctx, LedgerFilter{RepositoryID: repoID, State: FactAccepted})
	if err != nil {
		return nil, nil, err
	}
	for _, f := range newestPerID(rows) {
		if _, perr := ProjectPolicy([]InferredFact{f}, repoID); perr != nil {
			if k, ok := cascade.KindOf(perr); ok && k == cascade.KindPolicyDenied {
				denied = append(denied, f.ID)
				continue
			}
			return nil, nil, perr
		}
		truth := TruthDetectorCorroborated
		if f.AcceptedBy == AcceptedByOperator {
			truth = TruthOperatorAccepted
		}
		facts = append(facts, AcceptedFact{
			ID: f.ID, Version: f.Version, Subject: f.Subject, Fact: f.Fact, Truth: truth,
			Provenance: provider.JoinProvenance(provider.Provenance(f.Source)), AcceptedBy: f.AcceptedBy,
		})
	}
	return facts, denied, nil
}

// newestPerID keeps the highest version of each id, in List's id order.
func newestPerID(rows []InferredFact) []InferredFact {
	var out []InferredFact
	for _, r := range rows {
		if n := len(out); n > 0 && out[n-1].ID == r.ID {
			if r.Version > out[n-1].Version {
				out[n-1] = r
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// nextVersions maps each fact id to its highest stored version.
func nextVersions(rows []InferredFact) map[string]int {
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.ID] = max(out[r.ID], r.Version)
	}
	return out
}
