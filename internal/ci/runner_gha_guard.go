package ci

// Purpose: enforce admission and live public visibility before hosted dispatch.
// Inputs: admitted allow reader, live GitHub reader, job and origin.
// Outputs: repository coordinates or a journaled policy refusal.
// Constraints: no dispatch, cached visibility, resolver retry or lane fallback.
// SPORT: internal.ci.HostedGuard (P1-NODE-09).

import (
	"context"
	"errors"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrHostedExecutorRefused identifies a failed hosted execution policy check.
var ErrHostedExecutorRefused = cascade.New(cascade.KindPolicyDenied, "ci: hosted executor refused")

// HostedGuard checks the admitted ci.hosted.allow value and live visibility.
// Allowed must read the baseline-admitted value, never the on-disk config.
// Refusals and Attention connect refusals to the process journal and queue.
type HostedGuard struct {
	Allowed    func() (bool, error)
	Visibility GitHubReader
	Clock      runtime.Clock
	Refusals   func(context.Context, Refusal)
	Attention  AttentionPusher
}

// Check returns a GitHub repository only after every hosted guard passes.
// Errors and unknown visibility are refusals; visibility is read on every call.
func (g HostedGuard) Check(ctx context.Context, spec JobSpec, origin string) (owner, repo string, err error) {
	if g.Allowed == nil {
		return "", "", g.refuse(ctx, spec, "ci.hosted.allow is unset or not admitted")
	}
	allowed, err := g.Allowed()
	if err != nil || !allowed {
		return "", "", g.refuse(ctx, spec, "ci.hosted.allow must be admitted and true")
	}
	owner, repo, ok := parseGitHubOrigin(origin)
	if !ok || strings.TrimSpace(origin) != origin || strings.HasSuffix(origin, "/") {
		return "", "", g.refuse(ctx, spec, "origin must name an HTTPS or SSH github.com repository")
	}
	if spec.Sensitivity != provider.SensitivityPublic {
		return "", "", g.refuse(ctx, spec, "job sensitivity must be public")
	}
	if g.Visibility == nil {
		return "", "", g.refuse(ctx, spec, "live repository visibility is unknown; treated as private")
	}
	private, err := g.Visibility.RepoVisibility(ctx, owner, repo)
	if err != nil || private {
		return "", "", g.refuse(ctx, spec, "live repository visibility must be public; errors are treated as private")
	}
	return owner, repo, nil
}

// refuse records the failed condition without forwarding config or API payloads.
func (g HostedGuard) refuse(ctx context.Context, spec JobSpec, reason string) error {
	err := error(cascade.Wrap(cascade.KindPolicyDenied, ErrHostedExecutorRefused, reason))
	r := Refusal{JobID: spec.JobID, Kind: spec.Kind, Step: ExecutorGitHubActions, Reason: reason}
	if g.Clock != nil {
		r.At = g.Clock.Now()
	}
	if g.Refusals != nil {
		g.Refusals(ctx, r)
	}
	if g.Attention != nil {
		_, pushErr := g.Attention.Push(ctx, supervision.AttentionItem{
			Kind: supervision.KindError, SourceRef: "ci-hosted:" + spec.JobID + ":" + reason,
			ScopeRef: supervision.ScopeRef{Kind: scope.ScopeKindGlobal}, CreatedAt: r.At.UnixMilli(),
		})
		if pushErr != nil {
			err = errors.Join(err, cascade.Wrap(cascade.KindUnavailable, pushErr, "ci: hosted refusal attention"))
		}
	}
	return err
}
