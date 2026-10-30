// Purpose: the CLI's `review --pr` diff fetcher. When a daemon answers, the
//
//	fetch goes through its owner-only plugin.review.pr_diff verb, so the
//	CLI never opens the runtime store the daemon holds; when no daemon
//	answers, the in-process fetch path (internal/plugins) runs here, opening
//	the store once, as before P1-PLG-14.
//
// Inputs: the profile paths (resolved on the first call); dial and local
//
//	are test seams.
//
// Outputs: daemonOrLocalPRDiffFetcher, installed at init through
//
//	reviewplugin.SetPRDiffFetcher.
//
// Constraints: the mode is chosen once, on the first call, and kept. Only
//
//	a failure to reach the socket at all (isDialFailure) falls back to the
//	in-process path; any other daemon error is returned unchanged and the
//	mode stays unset. The reference is validated before any dial, and
//	nothing touches the store before the daemon has been tried.
//
// SPORT: cmd/cascade review --pr fetcher (ADD, P1-PLG-14, P1-BF-R305).
package main

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/plugins/github/tools"
	reviewplugin "github.com/acamarata/cascade/plugins/review"
)

// reviewPRDiffTimeout bounds one daemon fetch: a plugin launch, one GitHub
// GET of up to 8 MiB and the plugin's close.
const reviewPRDiffTimeout = 2 * time.Minute

func init() {
	// The fetcher is non-nil by construction, the only refusal
	// SetPRDiffFetcher has, so the error cannot occur.
	_ = reviewplugin.SetPRDiffFetcher(&daemonOrLocalPRDiffFetcher{local: plugins.InProcessPRDiffFetcher()})
}

type daemonOrLocalPRDiffFetcher struct {
	// dial returns the daemon caller; nil uses the profile's socket.
	dial func() (gateCall, error)
	// local is the in-process fetch path.
	local reviewplugin.PRDiffFetcher

	mu   sync.Mutex
	mode int
	call gateCall
}

// FetchPRDiff implements reviewplugin.PRDiffFetcher.
func (f *daemonOrLocalPRDiffFetcher) FetchPRDiff(ctx context.Context, owner, repo string, number int) ([]byte, error) {
	p := reviewPRDiffParams{Owner: owner, Repo: repo, Number: number}
	if err := p.validate(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.mode {
	case gateModeDaemon:
		return prDiffViaDaemon(ctx, f.call, p)
	case gateModeDirect:
		return f.local.FetchPRDiff(ctx, owner, repo, number)
	}
	call, err := f.daemonCaller()
	if err != nil {
		return nil, err
	}
	diff, err := prDiffViaDaemon(ctx, call, p)
	if err == nil {
		f.mode, f.call = gateModeDaemon, call
		return diff, nil
	}
	if !isDialFailure(err) {
		return nil, err
	}
	f.mode = gateModeDirect
	return f.local.FetchPRDiff(ctx, owner, repo, number)
}

// prDiffViaDaemon sends one reference and checks the reply's cap and size.
func prDiffViaDaemon(ctx context.Context, call gateCall, p reviewPRDiffParams) ([]byte, error) {
	var reply tools.PRDiff
	if err := call(ctx, reviewPRDiffMethod, p, &reply); err != nil {
		return nil, err
	}
	return checkPRDiffReply(reply)
}

func (f *daemonOrLocalPRDiffFetcher) daemonCaller() (gateCall, error) {
	if f.dial != nil {
		return f.dial()
	}
	paths, err := runtime.NewPathProvider(os.Getenv, os.UserHomeDir)
	if err != nil {
		return nil, err
	}
	settings, err := daemon.ResolveSettings(nil, paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(client.UnixDialer), reviewPRDiffTimeout).Do, nil
}
