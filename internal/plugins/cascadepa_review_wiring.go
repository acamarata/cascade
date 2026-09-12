package plugins

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/review"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// Purpose (this file): the composition-root wiring for cascade-pa's
//
//	`/memory review` in-chat commands and its digest nudge
//	(P1-E22-W5-S47-T4): injects real internal/client-backed
//	implementations of plugins/cascade-pa.ReviewChatClient and
//	plugins/cascade-pa.DigestSubscriber, matching
//	cascadepa_soul_wiring.go's identical pattern in this same package.
//
// CONTRACT NOTE: DigestSubscriber is a PULL seam over the real
//
//	memory.review.list RPC, not a push subscription to G/S-14.T3's
//	in-process DigestJob sink — see plugins/cascade-pa/events.go's own
//	doc comment for why no wire-level publish/subscribe exists for a
//	separate `cascade chat` process to attach to, and why this ticket
//	does not add one.
//
// SPORT: internal/plugins:cascadepa-review-wiring (ADD) — P1-E22-W5-S47-T4.
func init() {
	c := newCascadePAReviewClient(client.UnixDialer, cascadePAClientTimeout, runtime.NewDefaultPathProvider)
	cascadepa.SetReviewChatClient(c)
	cascadepa.SetDigestSubscriber(c)
}

// cascadePAReviewClient adapts internal/client's transport to both
// plugins/cascade-pa.ReviewChatClient and plugins/cascade-pa.
// DigestSubscriber — one real transport, two seams, since a digest check
// is simply a review.list read.
type cascadePAReviewClient struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
}

func newCascadePAReviewClient(dial client.DialFunc, timeout time.Duration, resolvePaths pathResolver) *cascadePAReviewClient {
	return &cascadePAReviewClient{dial: dial, timeout: timeout, resolvePaths: resolvePaths}
}

func (c *cascadePAReviewClient) rpcClient() (*client.Client, error) {
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, wrapReviewPathFailure(err)
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// List performs a real memory.review.list round trip, Section=pending.
func (c *cascadePAReviewClient) List(ctx context.Context) (cascadepa.ReviewChatListing, error) {
	rpcClient, err := c.rpcClient()
	if err != nil {
		return cascadepa.ReviewChatListing{}, err
	}
	var res review.ListResult
	params := review.ListParams{Section: review.SectionPending}
	if err := rpcClient.Do(ctx, review.MethodReviewList, params, &res); err != nil {
		return cascadepa.ReviewChatListing{}, err
	}
	out := cascadepa.ReviewChatListing{}
	for _, p := range res.Pending {
		out.Pending = append(out.Pending, cascadepa.ReviewChatCandidate{
			ID: p.ID, Kind: string(p.Kind), Sessions: p.Sessions, RefCount: p.RefCount,
		})
	}
	return out, nil
}

// Act performs a real memory.review.act round trip.
func (c *cascadePAReviewClient) Act(ctx context.Context, id, action string) (cascadepa.ReviewChatActResult, error) {
	rpcClient, err := c.rpcClient()
	if err != nil {
		return cascadepa.ReviewChatActResult{}, err
	}
	var res review.ActResult
	params := review.ActParams{ID: id, Action: action}
	if err := rpcClient.Do(ctx, review.MethodReviewAct, params, &res); err != nil {
		return cascadepa.ReviewChatActResult{}, err
	}
	return cascadepa.ReviewChatActResult{Action: res.Action, Changed: res.Changed}, nil
}

// Forget performs a real memory.forget round trip — the distinct method,
// never review.act.
func (c *cascadePAReviewClient) Forget(ctx context.Context, id string) (cascadepa.ReviewChatForgetResult, error) {
	rpcClient, err := c.rpcClient()
	if err != nil {
		return cascadepa.ReviewChatForgetResult{}, err
	}
	var res memory.ForgetResult
	if err := rpcClient.Do(ctx, memory.MethodForget, memory.ForgetParams{ID: id}, &res); err != nil {
		return cascadepa.ReviewChatForgetResult{}, err
	}
	return cascadepa.ReviewChatForgetResult{Forgotten: res.Forgotten}, nil
}

// CheckDigest reads the live pending count over the same review.list RPC
// List uses. See this file's package doc comment for why this is a pull,
// not a subscription to the daemon's in-process weekly cron.
func (c *cascadePAReviewClient) CheckDigest(ctx context.Context) (cascadepa.DigestSignal, error) {
	listing, err := c.List(ctx)
	if err != nil {
		return cascadepa.DigestSignal{}, err
	}
	return cascadepa.DigestSignal{PendingCount: len(listing.Pending)}, nil
}

// wrapReviewPathFailure classifies a path-resolution failure the same way
// cascadepa_soul_wiring.go's rpcClient does, split into its own function
// only so each file's rpcClient method stays trivially short.
func wrapReviewPathFailure(err error) error {
	return cascade.Wrap(cascade.KindUnavailable, err, "cascade chat: /memory review: resolve daemon socket path")
}
