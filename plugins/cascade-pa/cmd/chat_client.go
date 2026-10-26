package cmd

// Purpose (this file): the Client seam `cascade chat`'s one-shot/TUI
//   modes dial (OneShot/Stream), split out of chat.go under Art.10.3's
//   300-line cap once U/S-46.T4's non-interactive `--thread <slug>`
//   parity check pushed chat.go over it. See chat.go's package doc
//   comment for the COMPOSITION-ROOT DEVIATION this seam shares with
//   TopicsThreadsClient (topics_cli.go).
// Inputs: none at declaration; a real implementation is injected via
//   SetClient by the composition root (internal/plugins/
//   cascadepa_wiring.go), or a fake by a test.
// Outputs: Client, unconfiguredClient's typed refusal, and the
//   package-level accessor pair every call site uses.
// SPORT: plugins/cascade-pa:cmd:chat (CHANGE) — P1-E21-W5-S46-T4.

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Client is the seam over the daemon's chat adapter (T2's
// internal/conversation.Adapter, reached over the unix socket via
// internal/client): OneShot performs a single non-streaming turn (used by
// one-shot mode); Stream performs the same turn but delivers the response
// incrementally over the returned channel (used by TUI mode). Both take a
// context so a caller can cancel a live request (Ctrl-C mid-stream).
type Client interface {
	// OneShot sends req and returns the complete reply, or a typed
	// cascade error (KindUnavailable when the daemon cannot be reached,
	// KindNotFound when req.Thread does not exist, KindInvalidInput for a
	// malformed request).
	OneShot(ctx context.Context, req OneShotRequest) (OneShotResult, error)
	// Stream sends req and delivers the reply incrementally: tokens on
	// the first channel (closed when the stream ends normally), a single
	// terminal error (or nil) on the second channel once the first
	// closes. Canceling ctx aborts the stream; Stream must still close
	// both channels promptly in that case rather than leaking the
	// goroutine that feeds them.
	Stream(ctx context.Context, req OneShotRequest) (<-chan string, <-chan error)
	// Resume performs one V/S-47.T2 chat.resume call into req.Thread (the
	// session thread): the composed block to send as the next append's
	// leading context, or a typed error (KindNotFound for an unknown
	// topic or session thread, KindPolicyDenied for a source more
	// restrictive than the session, KindUnavailable when the daemon, its
	// resume service or recall.what's conversation leg is unavailable).
	Resume(ctx context.Context, req ResumeRequest) (ResumeResult, error)
	// CreateThread is chat.create_thread: the daemon mints a thread id and
	// writes the thread and its §5.16 marker in one transaction.
	// privacyMode is REQUIRED (one of the four tier names; the session
	// passes "restricted" when no flag was given). Returns the id, or a
	// typed error (KindInvalidInput for an unknown tier, KindUnavailable
	// when the daemon is unreachable).
	CreateThread(ctx context.Context, privacyMode string) (threadID string, err error)
}

// unconfiguredClient is the default Client: every call fails with a typed,
// actionable KindUnavailable error naming the missing wiring. This is not
// a stub standing in for unfinished work (Art.1) — it is the correct,
// deliberate behavior of a real binary that has not called SetClient, and
// its error is exactly what the daemon-unreachable acceptance criterion
// requires: never a blank screen, a hang, or a silent retry loop.
type unconfiguredClient struct{}

// errClientUnconfigured is the shared error unconfiguredClient returns.
var errClientUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade chat: no daemon adapter is wired into this binary; "+
		"start the daemon with `cascade daemon run` and ensure cascade-pa's "+
		"client wiring is configured")

func (unconfiguredClient) OneShot(context.Context, OneShotRequest) (OneShotResult, error) {
	return OneShotResult{}, errClientUnconfigured
}

func (unconfiguredClient) Stream(context.Context, OneShotRequest) (<-chan string, <-chan error) {
	tokens := make(chan string)
	errs := make(chan error, 1)
	close(tokens)
	errs <- errClientUnconfigured
	close(errs)
	return tokens, errs
}

func (unconfiguredClient) Resume(context.Context, ResumeRequest) (ResumeResult, error) {
	return ResumeResult{}, errClientUnconfigured
}

func (unconfiguredClient) CreateThread(context.Context, string) (string, error) {
	return "", errClientUnconfigured
}

// clientState guards the package-level Client seam so SetClient is safe
// under concurrent registration/test use.
var clientState struct {
	mu sync.RWMutex
	c  Client
}

// SetClient injects the real Client implementation. Intended to be called
// exactly once, by the composition root, before any `cascade chat`
// invocation — see chat.go's package doc comment for why that call site
// is a recorded, out-of-scope deviation in this ticket. Tests call it
// directly to inject a fake.
func SetClient(c Client) {
	clientState.mu.Lock()
	clientState.c = c
	clientState.mu.Unlock()
}

// activeClient returns the configured Client, or unconfiguredClient{} if
// SetClient has never been called.
func activeClient() Client {
	clientState.mu.RLock()
	defer clientState.mu.RUnlock()
	if clientState.c == nil {
		return unconfiguredClient{}
	}
	return clientState.c
}
