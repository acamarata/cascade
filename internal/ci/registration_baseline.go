package ci

// Purpose: select the operator's initial trusted branch tips.
// Inputs: local branch facts and an injected owned-remote predicate.
// Outputs: selected and excluded tips for one registration.
// Constraints: remote probes only expand URLs and never contact a remote.
// SPORT: registration baseline selection.

import (
	"context"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// OwnedRemoteFunc tests whether an upstream URL belongs to an owned namespace.
type OwnedRemoteFunc func(url string) bool

// BaselinePolicy selects explicitly named branches or the checked-out branch.
type BaselinePolicy struct {
	Owned    OwnedRemoteFunc
	Explicit []string
}

// ExcludedTip identifies a branch omitted from the registration baseline.
type ExcludedTip struct {
	Tip    jobs.BranchTip
	Reason string
}

// Baseline exclusion reasons are stable command output values.
const (
	// ExcludedNotCheckedOut identifies branches outside the default candidate.
	ExcludedNotCheckedOut = "not-checked-out"
	// ExcludedNotNamed identifies branches omitted by an explicit selection.
	ExcludedNotNamed = "not-named"
	// ExcludedPullRef identifies pull-request upstreams.
	ExcludedPullRef = "pull-ref"
	// ExcludedLocalUpstream identifies upstreams inside the same repository.
	ExcludedLocalUpstream = "local-upstream"
	// ExcludedMalformedUpstream identifies incomplete or malformed upstreams.
	ExcludedMalformedUpstream = "malformed-upstream"
	// ExcludedNoOwnedTest identifies upstreams without an ownership predicate.
	ExcludedNoOwnedTest = "no-owned-test"
	// ExcludedNotOwned identifies upstreams outside owned namespaces.
	ExcludedNotOwned = "not-owned"
)

type baselineDeps struct {
	tips         jobs.BranchTipsFunc
	config       func(context.Context, string) ([]byte, error)
	head         func(context.Context, string) (string, bool, error)
	effectiveURL func(context.Context, string, string) (string, error)
}

// Baseline holds the results of one registration's branch selection.
type Baseline struct {
	policy   BaselinePolicy
	deps     baselineDeps
	excluded []ExcludedTip
	detached bool
}

// RegistrationBaseline creates a local-only baseline selector.
func RegistrationBaseline(p BaselinePolicy) *Baseline {
	return registrationBaselineWith(p, baselineDeps{LocalBranchTips, gitUpstreamConfig, gitCheckedOutBranch, gitEffectiveURL})
}

func registrationBaselineWith(p BaselinePolicy, d baselineDeps) *Baseline {
	return &Baseline{policy: p, deps: d, excluded: make([]ExcludedTip, 0)}
}

// Excluded returns the latest selection's excluded branches, sorted by ref.
func (b *Baseline) Excluded() []ExcludedTip { return append([]ExcludedTip{}, b.excluded...) }

// Detached reports a detached HEAD under the latest successful default selection.
func (b *Baseline) Detached() bool { return b.detached }

// Tips selects baseline tips, clearing all prior results before every call.
func (b *Baseline) Tips(ctx context.Context, root string) (out []jobs.BranchTip, err error) {
	b.excluded, b.detached = make([]ExcludedTip, 0), false
	defer func() {
		if err != nil {
			b.excluded, b.detached = make([]ExcludedTip, 0), false
		}
	}()
	tips, err := b.deps.tips(ctx, root)
	if err != nil {
		return nil, err
	}
	sort.Slice(tips, func(i, j int) bool { return tips[i].Ref < tips[j].Ref })
	if len(b.policy.Explicit) > 0 {
		return b.explicit(tips)
	}
	head, detached, err := b.deps.head(ctx, root)
	if err != nil {
		return nil, err
	}
	if !detached && (!strings.HasPrefix(head, "refs/heads/") || len(head) == len("refs/heads/")) {
		return nil, cascade.New(cascade.KindIntegrity, "ci: malformed checked-out branch")
	}
	b.detached = detached
	out = make([]jobs.BranchTip, 0)
	var candidate *jobs.BranchTip
	for i := range tips {
		if !detached && tips[i].Ref == head {
			candidate = &tips[i]
		} else {
			b.excluded = append(b.excluded, ExcludedTip{tips[i], ExcludedNotCheckedOut})
		}
	}
	if candidate == nil {
		return out, nil
	}
	reason, err := b.upstream(ctx, root, strings.TrimPrefix(head, "refs/heads/"))
	if err != nil {
		return nil, err
	}
	if reason == "" {
		out = append(out, *candidate)
	} else {
		b.excluded = append(b.excluded, ExcludedTip{*candidate, reason})
	}
	sort.Slice(b.excluded, func(i, j int) bool { return b.excluded[i].Tip.Ref < b.excluded[j].Tip.Ref })
	return out, nil
}

func (b *Baseline) explicit(tips []jobs.BranchTip) ([]jobs.BranchTip, error) {
	named := make(map[string]bool)
	for _, name := range b.policy.Explicit {
		ref := name
		if !strings.HasPrefix(ref, "refs/heads/") {
			ref = "refs/heads/" + ref
		}
		found := false
		for _, tip := range tips {
			if tip.Ref == ref {
				found = true
			}
		}
		if !found {
			return nil, cascade.Newf(cascade.KindNotFound, "ci: baseline branch %s not found", name)
		}
		named[ref] = true
	}
	out := make([]jobs.BranchTip, 0)
	for _, tip := range tips {
		if named[tip.Ref] {
			out = append(out, tip)
		} else {
			b.excluded = append(b.excluded, ExcludedTip{tip, ExcludedNotNamed})
		}
	}
	return out, nil
}

func (b *Baseline) upstream(ctx context.Context, root, branch string) (string, error) {
	raw, err := b.deps.config(ctx, root)
	if err != nil {
		return "", err
	}
	config := make(map[string]string)
	for _, record := range strings.Split(string(raw), "\x00") {
		if record == "" {
			continue
		}
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			return "", cascade.New(cascade.KindIntegrity, "ci: malformed upstream config")
		}
		config[key] = value
	}
	remote, hasRemote := config["branch."+branch+".remote"]
	merge, hasMerge := config["branch."+branch+".merge"]
	if !hasRemote && !hasMerge {
		return "", nil
	}
	if strings.HasPrefix(merge, "refs/pull/") {
		return ExcludedPullRef, nil
	}
	if remote == "." {
		return ExcludedLocalUpstream, nil
	}
	if !hasRemote || !hasMerge || remote == "" || !strings.HasPrefix(merge, "refs/heads/") {
		return ExcludedMalformedUpstream, nil
	}
	if b.policy.Owned == nil {
		return ExcludedNoOwnedTest, nil
	}
	u, exists := config["remote."+remote+".url"]
	if !exists || u == "" || strings.ContainsAny(remote, ":/\\") {
		return ExcludedNotOwned, nil
	}
	effective, err := b.deps.effectiveURL(ctx, root, remote)
	if err != nil {
		return "", err
	}
	rawOwned, effectiveOwned := b.policy.Owned(u), b.policy.Owned(effective)
	if !rawOwned || !effectiveOwned {
		return ExcludedNotOwned, nil
	}
	return "", nil
}
