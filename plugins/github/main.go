// Command cascade-github is the process-tier GitHub plugin: an OAuth
// broker plus MCP tool groups for repositories, issues and pull requests.
//
// Purpose: speak the host's stdio JSON-RPC protocol — answer the
//
//	cascade.hello handshake, perform tool calls against the GitHub API, and
//	hand the OAuth token to the host for storage.
//
// Inputs: newline-delimited JSON-RPC frames on stdin.
// Outputs: newline-delimited JSON-RPC frames on stdout; diagnostics on
//
//	stderr, which the host captures as this plugin's stderr tail.
//
// Constraints: imports pkg/** and this plugin's own packages ONLY, never
//
//	internal/** (Art.10.2). This is a PROCESS-tier plugin: it is launched
//	from its manifest by the host's ProcessRuntime and is never registered
//	in the compile-time builtin registry. Two tests hold that: main_test.go
//	asserts linking THIS package registers nothing, and
//	internal/plugins/builtin_tier_only_test.go asserts the registry carries
//	no cascade-github entry where that registry is actually populated.
//
//	Direct GitHub API egress is this plugin's declared, accepted-risk
//	design (trusted tier, single net scope api.github.com): the plugin
//	performs its own calls rather than describing them for the host.
//
//	The token reaches the vault through a host_secret_ref host-fn
//	notification and is never written into the manifest or any config
//	file. The host's process ABI has no response path for a
//	plugin-initiated call (the id is discarded), so host_secret_ref is
//	used to STORE, which is one-way; the token this process uses for its
//	own API calls is the one the exchange just returned, held in memory
//	for the life of the process and never logged.
//
//	P1-E25-W5-S51-T3's `github-ci-wait`/`github.ci.merge-on-green` manifest
//	commands need NO new dispatch code here: wait-on-green polls
//	api.github.com through the HOST's own internal/ci.Client (never this
//	process), and merge-on-green's ONLY call into this process is
//	"cascade-github.prs.merge" — already reachable through the tool-prefix
//	branch in dispatch, below, since T1 registered it. The host-side
//	orchestration (poll loop, L3 classify, deny-list, grant check,
//	audit) lives in internal/ci/waitmerge_{wait,merge}.go and the
//	internal/plugins/ci_waitmerge_wiring.go bridge — never in this
//	process, per Art.10.2 (this file may not import internal/**).
//
//	P1-E25-W5-S51-T4's `github-ci-watch-add|list|remove` manifest commands
//	likewise need NO new dispatch code here: they read/write [ci.watch] in
//	config.toml and push to the R/S-39.T1 attention queue, both entirely
//	host-side (cmd/cascade/github_ci_watch_cmd.go) — this plugin process is
//	never launched to serve them.
//
// SPORT: plugins/github (ADD) — P1-E25-W5-S51-T1; github-ci-wait/
//
//	github.ci.merge-on-green manifest commands (CHANGE) — P1-E25-W5-S51-T3;
//	github-ci-watch-add|list|remove manifest commands (CHANGE) —
//	P1-E25-W5-S51-T4. All are MOUNTED by the host's process-tier command
//	mount (cmd/cascade/plugin_process_mount.go); the dotted name is what
//	lets `merge-on-green` stay one path segment.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/auth"
	"github.com/acamarata/cascade/plugins/github/tools"
)

// ProtocolVersion is the stdio protocol revision this plugin speaks. It
// must match the host's PluginProtocolVersion; the host refuses a plugin
// that reports lower.
const ProtocolVersion = "1.0.0"

// PluginName is this plugin's manifest id and tool namespace root.
const PluginName = "cascade-github"

// frame is one JSON-RPC 2.0 message in either direction. A request carries
// an id; a notification does not.
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *uint64         `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// helloAck is the handshake response shape the host decodes.
type helloAck struct {
	ProtocolVersion string `json:"protocol_version"`
	ManifestHash    string `json:"manifest_hash"`
}

// broker is one running instance: the API transport, the token it holds,
// the in-flight authorization, and the stream it writes notifications to.
type broker struct {
	doer  tools.Doer
	post  auth.Poster
	flows flowState
	out   io.Writer

	mu    sync.Mutex
	token string
}

func main() {
	p := &broker{doer: tools.HTTPDoer{}, post: HTTPPoster}
	if err := p.run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "cascade-github: %v\n", err)
		os.Exit(1)
	}
}

// setToken arms this process's API client with a token.
func (p *broker) setToken(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = token
}

// client returns an API client carrying whatever token this process holds.
func (p *broker) client() tools.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	return tools.Client{Doer: p.doer, Token: p.token}
}

// emit writes one pre-encoded frame (a notification) to stdout.
func (p *broker) emit(encoded []byte) error {
	if p.out == nil {
		return cascade.New(cascade.KindInternal, "github: no output stream")
	}
	if _, err := p.out.Write(append(encoded, '\n')); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "github: writing a notification")
	}
	return nil
}

// run reads frames until stdin closes. A decode failure on one frame is
// skipped rather than fatal: the host owns the stream's framing, and one
// unreadable line must not take down a plugin that can still serve the
// next call.
func (p *broker) run(stdin io.Reader, stdout, stderr io.Writer) error {
	p.out = stdout
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(stdout)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var in frame
		if err := json.Unmarshal(line, &in); err != nil {
			_, _ = fmt.Fprintf(stderr, "cascade-github: skipping an undecodable frame: %v\n", err)
			continue
		}
		out, reply := p.dispatch(in)
		if !reply {
			continue
		}
		if err := enc.Encode(out); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "github: writing a reply")
		}
	}
	if err := scanner.Err(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "github: reading the host stream")
	}
	return nil
}

// dispatch answers one inbound frame. reply is false for a frame that needs
// no response: a notification carries no id, and replying to one would put
// a frame on the wire the host never correlates.
func (p *broker) dispatch(in frame) (out frame, reply bool) {
	if in.ID == nil {
		return frame{}, false
	}
	switch {
	case in.Method == "cascade.hello":
		return frame{
			JSONRPC: "2.0",
			ID:      in.ID,
			Result:  helloAck{ProtocolVersion: ProtocolVersion, ManifestHash: manifestHash()},
		}, true
	case in.Method == "cascade.auth.begin":
		return p.authBeginReply(in)
	case in.Method == "cascade.auth.complete":
		return p.authCompleteReply(in)
	case strings.HasPrefix(in.Method, PluginName+"."):
		return p.toolReply(in)
	default:
		return frame{
			JSONRPC: "2.0",
			ID:      in.ID,
			Error: &rpcError{
				Code:    -32601,
				Message: fmt.Sprintf("cascade-github: no such method %q", in.Method),
			},
		}, true
	}
}

// manifestHash reports the manifest digest this build was compiled against.
//
// It is empty today and deliberately so: the host records what the plugin
// reports and does not verify it, so returning a fabricated digest would
// put a value into the host's records that means nothing. An empty string
// says "this plugin does not attest its manifest", which is true.
func manifestHash() string { return "" }

// StoreTokenNotification builds the host_secret_ref frame that hands a
// token to the vault.
//
// It is a NOTIFICATION, with no id, because the host's process ABI has no
// response path for a plugin-initiated call: an id would be discarded and
// the plugin would wait for a reply that never comes. Storing is one-way,
// which is all this needs.
//
// The token is never written to a file, a log line or the manifest: this
// frame is the only place it leaves the process, and it goes to the host's
// vault.
func StoreTokenNotification(key, token string) ([]byte, error) {
	if key == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "github: a secret key is required")
	}
	if token == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "github: refusing to store an empty token")
	}
	params, err := json.Marshal(map[string]string{"key": key, "value": token})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "github: encoding the secret-store call")
	}
	encoded, err := json.Marshal(frame{JSONRPC: "2.0", Method: "host_secret_ref", Params: params})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "github: encoding the host_secret_ref frame")
	}
	return encoded, nil
}

// TokenKey is the vault key this plugin stores its token under. It is
// derived from the plugin name so two harness plugins can never collide,
// and it names the provider rather than the user: one token per install.
func TokenKey() string { return PluginName + ".oauth_token" }

// toolReply performs one namespaced tool call and returns its result.
func (p *broker) toolReply(in frame) (frame, bool) {
	var args tools.Args
	if len(in.Params) > 0 {
		if err := json.Unmarshal(in.Params, &args); err != nil {
			return errorReply(in, err), true
		}
	}
	tool := strings.TrimPrefix(in.Method, PluginName+".")
	result, err := p.client().Call(context.Background(), tool, args)
	if err != nil {
		return errorReply(in, err), true
	}
	return frame{JSONRPC: "2.0", ID: in.ID, Result: result}, true
}

// errorReply renders err as a JSON-RPC error frame.
func errorReply(in frame, err error) frame {
	return frame{JSONRPC: "2.0", ID: in.ID, Error: &rpcError{Code: -32000, Message: err.Error()}}
}
