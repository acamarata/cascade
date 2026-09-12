package plugins

import (
	"context"
	"fmt"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// Purpose (this file): closes the disclosed composition-root gap from
//
//	P1-E20-W5-S43-T3 (recorded in internal/build/testonly-allow.json
//	naming plugins/cascade-pa/cmd.SetClient, expected_caller pointing at
//	this exact file path): injects a real internal/client-backed
//	implementation of plugins/cascade-pa/cmd.Client so `cascade chat`
//	reaches the daemon's unix-socket JSON-RPC transport instead of the
//	package default unconfiguredClient.
//
// Inputs: none at import time. Each call resolves the daemon socket path
//
//	lazily via runtime.NewDefaultPathProvider, mirroring
//	cmd/cascade/root_paths.go's lazyPaths deferred-resolution pattern so
//	importing this package never touches the environment.
//
// Outputs: registers a *cascadePAClient with plugins/cascade-pa/cmd via
//
//	SetClient. OneShot/Stream perform a real chat.append_turn round trip
//	(internal/conversation.MethodAppendTurn, the wire method T2's Adapter
//	registers) and then report, honestly, that no daemon component yet
//	generates an assistant reply -- see errReplyGenerationUnavailable.
//
// Constraints: this package (internal/plugins) is the one place allowed to
//
//	import both internal/** and plugins/** (Art.10.2) -- see this file's
//	sibling registry.go's own codex/opencode generator-wiring init() for
//	the identical precedent this follows.
//
// SPORT: internal/plugins:cascadepa-wiring (ADD) -- FIX-cascade-chat-client-wiring.

// cascadePAClientTimeout bounds every chat.append_turn round trip this
// adapter issues, matching cmd/cascade/status.go's statusDialTimeout
// precedent for the identical unix-socket transport.
const cascadePAClientTimeout = 5 * time.Second

func init() {
	pacmd.SetClient(newCascadePAClient(client.UnixDialer, cascadePAClientTimeout, runtime.NewDefaultPathProvider))
}

// pathResolver matches runtime.NewDefaultPathProvider's signature so tests
// can inject a fake without touching the real environment (Art.7.1).
type pathResolver func() (runtime.PathProvider, error)

// cascadePAClient adapts internal/client's unix-socket JSON-RPC transport
// to plugins/cascade-pa/cmd.Client. Path resolution is deferred to each
// call, never performed at init time, so importing this package (and thus
// running its init()) never touches the environment.
type cascadePAClient struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
}

// newCascadePAClient builds a cascadePAClient from its three collaborators,
// injected so tests substitute a fake dialer and a fake path resolver
// without a real socket or a real home directory.
func newCascadePAClient(dial client.DialFunc, timeout time.Duration, resolvePaths pathResolver) *cascadePAClient {
	return &cascadePAClient{dial: dial, timeout: timeout, resolvePaths: resolvePaths}
}

// appendTurnParams/appendSegmentWire/appendTurnResult mirror
// internal/conversation/adapter.go's wire shapes exactly. Duplicated
// rather than imported: that package's own types are unexported (they are
// the JSON-RPC server side's decode target, not a public SDK), so this
// client-side encode target is this adapter's own, matching
// internal/client's own documented practice of never importing
// internal/rpc's server-side literals (client.go's rpcPath doc comment).
type appendTurnParams struct {
	ThreadID string              `json:"thread_id"`
	Role     string              `json:"role"`
	Segments []appendSegmentWire `json:"segments"`
}

type appendSegmentWire struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

type appendTurnResult struct {
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
	Seq      int64  `json:"seq"`
}

// errReplyGenerationUnavailable is OneShot/Stream's honest outcome once
// the user's turn is genuinely appended over the real transport: no
// daemon component yet generates an assistant reply to a
// chat.append_turn call (internal/conversation.Adapter registers
// append_turn/get_thread/list_threads only -- there is no chat.generate
// or equivalent anywhere in the tree today). Returning fabricated reply
// content here would be exactly the Art.1 stub this phase forbids; this
// is real, disclosed, deliberate behavior, not a stand-in for missing
// wiring -- matching unconfiguredClient's own contract in
// plugins/cascade-pa/cmd/chat.go.
var errReplyGenerationUnavailable = cascade.New(cascade.KindUnsupported,
	"cascade chat: message recorded, but no daemon component generates an "+
		"assistant reply yet (internal/conversation.Adapter registers "+
		"chat.append_turn/get_thread/list_threads only)")

// rpcClient resolves the daemon socket path and builds an internal/client
// Client dialing it. A path-resolution failure is reported as
// KindUnavailable rather than panicking -- the real environment can always
// fail to resolve a home directory, and this call happens per-request, at
// invocation time, never at import/construction time.
func (c *cascadePAClient) rpcClient() (*client.Client, error) {
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade chat: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// OneShot appends req as a "user" turn via the real chat.append_turn RPC,
// then returns errReplyGenerationUnavailable naming the thread/turn that
// were genuinely recorded -- see that error's doc comment for why no
// content is fabricated. A transport failure (no daemon listening, a
// timeout, cancellation) returns internal/client's own classified taxonomy
// error instead, unmodified -- this is the signal that distinguishes a
// real, wired client from unconfiguredClient's canned message.
func (c *cascadePAClient) OneShot(ctx context.Context, req pacmd.OneShotRequest) (pacmd.OneShotResult, error) {
	rpc, err := c.rpcClient()
	if err != nil {
		return pacmd.OneShotResult{}, err
	}
	params := appendTurnParams{
		ThreadID: req.Thread,
		Role:     "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: req.Prompt}},
	}
	var result appendTurnResult
	if err := rpc.Do(ctx, conversation.MethodAppendTurn, params, &result); err != nil {
		return pacmd.OneShotResult{}, err
	}
	return pacmd.OneShotResult{}, cascade.Wrap(cascade.KindUnsupported, errReplyGenerationUnavailable,
		fmt.Sprintf("thread %s turn %s", result.ThreadID, result.TurnID))
}

// Stream performs the same real append as OneShot, asynchronously: tokens
// is always closed with zero tokens sent (no daemon component streams
// assistant output yet -- see errReplyGenerationUnavailable), and errs
// carries OneShot's outcome once tokens closes, matching
// unconfiguredClient.Stream's own channel-ordering contract.
func (c *cascadePAClient) Stream(ctx context.Context, req pacmd.OneShotRequest) (<-chan string, <-chan error) {
	tokens := make(chan string)
	errs := make(chan error, 1)
	go func() {
		_, err := c.OneShot(ctx, req)
		close(tokens)
		errs <- err
		close(errs)
	}()
	return tokens, errs
}
