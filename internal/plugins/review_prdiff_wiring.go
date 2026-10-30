package plugins

// Purpose (this file): the one --pr diff fetch path. A request first
// transits the github-pr-diff egress class through the process firewall,
// then the trusted cascade-github process plugin is launched (recorded by
// TrustedHandle), asked for cascade-github.prs.diff once, and closed.
// No other fetch path exists: no HTTP client, no gh, no git.
//
// SPORT: internal/plugins:review_prdiff_wiring (ADD) — P1-PLG-05.

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/tools"
	reviewplugin "github.com/acamarata/cascade/plugins/review"
)

// prDiffMethod is the plugin tool the fetch calls.
const prDiffMethod = "cascade-github.prs.diff"

func init() {
	// The fetcher is non-nil by construction, the only refusal
	// SetPRDiffFetcher has, so the error cannot occur.
	_ = reviewplugin.SetPRDiffFetcher(newPRDiffFetcher(InstalledHandleProvider(), egress.DefaultRegistry()))
}

// prDiffParams is the request JSON {owner, repo, number}.
type prDiffParams struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

type prDiffIntercept func(context.Context, *egress.Registry, []byte) ([]byte, error)

type prDiffFetcher struct {
	provider  HandleProvider
	registry  *egress.Registry
	intercept prDiffIntercept
}

func newPRDiffFetcher(provider HandleProvider, registry *egress.Registry) *prDiffFetcher {
	return &prDiffFetcher{provider: provider, registry: registry, intercept: interceptPRDiff}
}

// interceptPRDiff runs params through the process firewall under
// github-pr-diff at tier internal. An unset engine factory is
// ErrNoProcessFirewall (KindUnavailable); nothing falls back to another
// engine.
func interceptPRDiff(ctx context.Context, registry *egress.Registry, params []byte) ([]byte, error) {
	engine, err := egress.ProcessEngine(registry)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "review: the PR diff fetch needs the process firewall")
	}
	return engine.InterceptClass(ctx, egress.EgressClassGitHubPRDiff, egress.TierInternal, params)
}

// FetchPRDiff implements reviewplugin.PRDiffFetcher: validate, intercept,
// launch, call, close. Every refusal is typed and happens before the next
// step starts.
func (f *prDiffFetcher) FetchPRDiff(ctx context.Context, owner, repo string, number int) ([]byte, error) {
	if _, err := tools.PRDiffRequest(owner, repo, number); err != nil {
		return nil, err
	}
	params, err := json.Marshal(prDiffParams{Owner: owner, Repo: repo, Number: number})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "review: encoding the PR reference")
	}
	sent, err := f.intercept(ctx, f.registry, params)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(sent, params) {
		return nil, cascade.New(cascade.KindPolicyDenied,
			"review: the egress firewall altered the PR reference; refusing to fetch a pull request other than the one named")
	}
	h, err := f.launch(ctx)
	if err != nil {
		return nil, err
	}
	defer closeTrusted(ctx, h)
	out, err := h.Call(ctx, prDiffMethod, sent)
	if err != nil {
		return nil, err
	}
	return decodePRDiffResult(out)
}

// launch starts cascade-github. A missing install (KindNotFound from
// LaunchInstalled) becomes KindUnavailable naming the add command; a
// disabled or untrusted install keeps LaunchInstalled's error unchanged.
func (f *prDiffFetcher) launch(ctx context.Context) (*TrustedHandle, error) {
	if f.provider == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "review: the PR diff fetcher has no HandleProvider")
	}
	h, err := f.provider(ctx, cascadeGitHubPluginID)
	if err != nil {
		if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindNotFound {
			return nil, cascade.Wrap(cascade.KindUnavailable, err,
				"review: --pr needs the cascade-github plugin; run `cascade plugin add cascade-github`")
		}
		return nil, err
	}
	if h == nil {
		return nil, cascade.New(cascade.KindInternal, "plugins: the HandleProvider returned no Handle and no error")
	}
	return h, nil
}

// closeTrusted stops h within closeGrace, even after ctx is cancelled.
func closeTrusted(ctx context.Context, h *TrustedHandle) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeGrace)
	defer cancel()
	_ = h.Close(closeCtx)
}

// decodePRDiffResult decodes the plugin's tools.PRDiff, refuses one whose
// Bytes does not match its Diff (KindIntegrity), and re-applies the 8 MiB
// cap on the host side through tools.DecodePRDiff.
func decodePRDiffResult(out []byte) ([]byte, error) {
	var res tools.PRDiff
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "review: decoding the cascade-github.prs.diff result")
	}
	if res.Bytes != len(res.Diff) {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"review: cascade-github.prs.diff reported %d bytes but sent %d", res.Bytes, len(res.Diff))
	}
	checked, err := tools.DecodePRDiff([]byte(res.Diff))
	if err != nil {
		return nil, err
	}
	return []byte(checked.Diff), nil
}
