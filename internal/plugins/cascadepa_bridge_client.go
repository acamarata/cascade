package plugins

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// Purpose (this file): `cascade pa pair`'s client — the CLI half of issuance,
//   which asks the DAEMON for a code over pa.pair_code instead of opening the
//   bridge's database and starting a poll loop of its own.
//
// WHY IT IS AN RPC AND NOT A LOCAL STORE READ. Verification happens in the
//   daemon, on the poll goroutine. The code's digest is keyed from the bot
//   token, so issuing locally would mean either handing the CLI process the
//   token (a second reader of the credential, on a path with no poll loop to
//   need it) or storing an unkeyed digest a stolen database could brute-force.
//   One process owns the credential and the verifier; the CLI asks it. This is
//   the same shape cascadepa_wiring.go's chat client has, for the same reason.
//
// Inputs: nothing at import time. The socket path is resolved per call through
//   runtime.NewDefaultPathProvider, so importing this package never touches the
//   environment.
// Outputs: SetPairClient wired to a real internal/client round trip.
// Constraints: a transport failure is returned UNMODIFIED — internal/client's
//   own classified error ("daemon not running or unreachable at <socket>") is
//   the honest answer for a host whose daemon is not up, and it is what
//   distinguishes a real wired client from the package's unconfigured default.
//
// SPORT: internal/plugins:cascadepa-bridge-client (ADD) — P1-E23-W5-S48-T1.

// pairClientTimeout bounds the pa.pair_code round trip, matching
// cascadePAClientTimeout's precedent for the identical unix-socket transport.
const pairClientTimeout = 5 * time.Second

func init() {
	pacmd.SetPairClient(newCascadePAPairClient(client.UnixDialer, pairClientTimeout, runtime.NewDefaultPathProvider))
}

// cascadePAPairClient implements pacmd.PairClient over the daemon socket.
type cascadePAPairClient struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	// doer, when non-nil, replaces the real client construction — see
	// rpcDoer's own doc comment in cascadepa_wiring.go for why this seam
	// exists and why it is always nil in production.
	doer rpcDoer
}

// newCascadePAPairClient builds the client from its three collaborators, all
// injected so a test substitutes a fake round trip and a fake path resolver.
func newCascadePAPairClient(dial client.DialFunc, timeout time.Duration,
	resolvePaths pathResolver) *cascadePAPairClient {
	return &cascadePAPairClient{dial: dial, timeout: timeout, resolvePaths: resolvePaths}
}

// pairCodeParams/pairCodeResult mirror internal/daemon's own wire shapes.
// Duplicated rather than imported for the reason cascadepa_wiring.go's
// appendTurnParams states: the server side's decode target is not a public SDK
// type, so the client-side encode target is this adapter's own.
type pairCodeParams struct {
	Subject string `json:"subject,omitempty"`
}

type pairCodeResult struct {
	Code      string `json:"code"`
	Subject   string `json:"subject"`
	ExpiresAt string `json:"expires_at"`
}

// rpcClient resolves the daemon socket and builds the transport.
func (c *cascadePAPairClient) rpcClient() (rpcDoer, error) {
	if c.doer != nil {
		return c.doer, nil
	}
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade pa pair: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// IssueCode asks the daemon to mint a code. An empty subject asks for the
// bridge the daemon actually runs; the daemon refuses any other subject by
// name, so no unverifiable code is ever printed.
func (c *cascadePAPairClient) IssueCode(ctx context.Context, subject string) (pacmd.PairCodeResult, error) {
	rpc, err := c.rpcClient()
	if err != nil {
		return pacmd.PairCodeResult{}, err
	}
	var out pairCodeResult
	if err := rpc.Do(ctx, daemon.MethodBridgePairCode, pairCodeParams{Subject: subject}, &out); err != nil {
		return pacmd.PairCodeResult{}, err
	}
	expires, err := time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil {
		return pacmd.PairCodeResult{}, cascade.Wrapf(cascade.KindIntegrity, err,
			"cascade pa pair: the daemon returned an unreadable expiry %q", out.ExpiresAt)
	}
	return pacmd.PairCodeResult{Code: out.Code, Subject: out.Subject, ExpiresAt: expires}, nil
}
