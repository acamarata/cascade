// Purpose: `review --pr`'s diff fetch on the wire: the daemon verb
//
//	plugin.review.pr_diff, which runs the in-process fetch path
//	(internal/plugins InProcessPRDiffFetcher) under the daemon's own plugin
//	launch environment, so a CLI never opens the single-opener runtime store
//	while a daemon holds it.
//
// Inputs: reviewPRDiffParams {owner, repo, number}.
// Outputs: reviewPRDiffParams.validate, reviewPRDiffHandler, checkPRDiffReply.
//
// Constraints: FAIL CLOSED. Only the owner CLI may call it (declared
//
//	AccessOwnerOnly beside its registration and checked again here, before
//	any decode). The request is strictly decoded (no unknown field, no
//	trailing bytes, at most reviewPRDiffMaxRequest bytes) over a closed
//	grammar and validated before any launch; a token bucket bounds the rate.
//	The reply is tools.PRDiff and never exceeds tools.MaxPRDiffBytes; no
//	error carries the diff body.
//
// SPORT: cmd/cascade plugin.review.pr_diff (ADD, P1-PLG-14, P1-BF-R305).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"regexp"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/tools"
	reviewplugin "github.com/acamarata/cascade/plugins/review"
)

// reviewPRDiffMethod is the daemon verb that fetches one pull request diff.
const reviewPRDiffMethod = "plugin.review.pr_diff"

// reviewPRDiffMaxRequest bounds one encoded request on the wire.
const reviewPRDiffMaxRequest = 512

var (
	prDiffOwnerPat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}$`)
	prDiffRepoPat  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// reviewPRDiffParams is the verb's whole request.
type reviewPRDiffParams struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func prDiffInvalid(msg string) error {
	return cascade.New(cascade.KindInvalidInput, reviewPRDiffMethod+": "+msg)
}

// validate refuses any reference outside the closed grammar, then applies
// the fetch path's own reference check, so both modes refuse alike.
func (p reviewPRDiffParams) validate() error {
	if !prDiffOwnerPat.MatchString(p.Owner) {
		return prDiffInvalid("the owner is not a GitHub account name")
	}
	if !prDiffRepoPat.MatchString(p.Repo) || p.Repo == "." || p.Repo == ".." {
		return prDiffInvalid("the repo is not a GitHub repository name")
	}
	if p.Number <= 0 || p.Number > math.MaxInt32 {
		return prDiffInvalid("the number is not a pull request number")
	}
	if _, err := tools.PRDiffRequest(p.Owner, p.Repo, p.Number); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, reviewPRDiffMethod+": the reference is refused")
	}
	return nil
}

// checkPRDiffReply refuses a reply over the cap or whose Bytes does not
// match its Diff, and returns the diff bytes.
func checkPRDiffReply(r tools.PRDiff) ([]byte, error) {
	if len(r.Diff) > tools.MaxPRDiffBytes {
		return nil, prDiffInvalid("the pull request diff exceeds 8 MiB")
	}
	if r.Bytes != len(r.Diff) {
		return nil, cascade.Newf(cascade.KindIntegrity, "%s: the reply reported %d bytes but carried %d", reviewPRDiffMethod, r.Bytes, len(r.Diff))
	}
	return []byte(r.Diff), nil
}

// callerIsOwnerCLI mirrors rpc.AccessOwnerOnly: a transport-verified caller
// with no principal. An absent caller is never the owner.
func callerIsOwnerCLI(ctx context.Context) bool {
	c, ok := principal.CallerFrom(ctx)
	return ok && c.Principal == nil
}

// reviewPRDiffHandler is the daemon side of plugin.review.pr_diff over the
// in-process fetcher fetch.
func reviewPRDiffHandler(fetch reviewplugin.PRDiffFetcher, clock runtime.Clock) func(context.Context, json.RawMessage) (any, error) {
	bucket := &gateAppendBucket{}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if !callerIsOwnerCLI(ctx) {
			return nil, cascade.New(cascade.KindPermissionDenied, reviewPRDiffMethod+": only the owner CLI may fetch a pull request diff")
		}
		if len(raw) > reviewPRDiffMaxRequest {
			return nil, prDiffInvalid("the request is too large")
		}
		var p reviewPRDiffParams
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil || dec.Decode(&struct{}{}) != io.EOF {
			return nil, prDiffInvalid("params are not one {owner, repo, number} reference")
		}
		if err := p.validate(); err != nil {
			return nil, err
		}
		if !bucket.admit(clock.Now()) {
			return nil, cascade.New(cascade.KindQuotaExhausted, reviewPRDiffMethod+": too many fetches; retry shortly")
		}
		diff, err := fetch.FetchPRDiff(ctx, p.Owner, p.Repo, p.Number)
		if err != nil {
			return nil, err
		}
		reply := tools.PRDiff{Diff: string(diff), Bytes: len(diff)}
		if _, err := checkPRDiffReply(reply); err != nil {
			return nil, err
		}
		return reply, nil
	}
}
