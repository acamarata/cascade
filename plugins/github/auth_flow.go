// Purpose (this file): the plugin's two auth verbs — begin a PKCE flow,
//
//	and complete it: wait for the callback, redeem the code, hand the token
//	to the vault, and arm this process's API client with it.
//
// Inputs: cascade.auth.begin / cascade.auth.complete frames.
// Outputs: an authorization URL, then a host_secret_ref notification
//
//	carrying the token, written to stdout as a frame of its own.
//
// Constraints: the token is handed to the host and held in memory. It is
//
//	never written to a file, a log line or the manifest, and never returned
//	in a reply frame — a reply is correlated and recorded by the host, and
//	the vault is the only place a token belongs. The PKCE verifier never
//	leaves this process at all.
//
// SPORT: plugins/github:auth-flow (ADD) — P1-E25-W5-S51-T1.
package main

import (
	"context"
	"encoding/json"
	"os"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/auth"
)

// callbackWaiter is the listener half of a flow. *auth.Loopback is the
// production implementation; the seam exists so this package's unit tests
// can drive a whole authorization without binding a socket, which Art.7.2's
// no-network unit lane is there to keep out.
type callbackWaiter interface {
	// RedirectURI is the address the authorization server redirects to.
	RedirectURI() string
	// Wait blocks for the callback and verifies it against the PKCE state.
	Wait(ctx context.Context, p auth.PKCE) (string, error)
	// Close releases the listener.
	Close() error
}

// listenLoopback is the production listener factory.
func listenLoopback() (callbackWaiter, error) { return auth.Listen() }

// pendingFlow is an authorization in progress: the values that started it,
// and the listener waiting for it to come back.
type pendingFlow struct {
	pkce     auth.PKCE
	loopback callbackWaiter
	clientID string
}

// flowState holds the one in-flight authorization.
//
// One at a time is deliberate. A second concurrent flow would leave two
// listeners bound and two states live, and the callback carries no way to
// say which flow it belongs to beyond its state — so beginning a new flow
// replaces the old one rather than racing it.
type flowState struct {
	// listen binds a callback listener. A nil listen means the real
	// loopback one.
	listen func() (callbackWaiter, error)

	mu      sync.Mutex
	pending *pendingFlow
}

// listener returns the configured factory, or the production one.
func (s *flowState) listener() func() (callbackWaiter, error) {
	if s.listen != nil {
		return s.listen
	}
	return listenLoopback
}

// begin starts a flow and returns the authorization URL.
func (s *flowState) begin(clientID string) (map[string]string, error) {
	if err := auth.RequireInteractive(os.Getenv); err != nil {
		return nil, err
	}
	cfg, err := auth.Config(clientID)
	if err != nil {
		return nil, err
	}
	pkce, err := auth.NewPKCE()
	if err != nil {
		return nil, err
	}
	loopback, err := s.listener()()
	if err != nil {
		return nil, err
	}
	url, err := auth.AuthorizeURL(cfg, pkce, loopback.RedirectURI())
	if err != nil {
		_ = loopback.Close()
		return nil, err
	}

	s.replace(&pendingFlow{pkce: pkce, loopback: loopback, clientID: clientID})

	// The verifier is deliberately absent: it is the secret half of the
	// proof key and belongs only in this process's memory.
	return map[string]string{
		"authorize_url": url,
		"state":         pkce.State,
		"scope":         auth.RequiredScope,
		"redirect_uri":  loopback.RedirectURI(),
	}, nil
}

// replace installs flow as the pending one, closing any listener the
// previous flow left bound.
func (s *flowState) replace(flow *pendingFlow) {
	s.mu.Lock()
	previous := s.pending
	s.pending = flow
	s.mu.Unlock()
	if previous != nil {
		_ = previous.loopback.Close()
	}
}

// take removes and returns the pending flow, so a completion can only be
// run once against it.
func (s *flowState) take() *pendingFlow {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow := s.pending
	s.pending = nil
	return flow
}

// complete waits for the callback, redeems the code, and returns the token
// together with the notification that stores it.
func (s *flowState) complete(ctx context.Context, post auth.Poster) (token string, store []byte, err error) {
	flow := s.take()
	if flow == nil {
		return "", nil, cascade.New(cascade.KindConflict,
			"github: no authorization is in progress; call cascade.auth.begin first")
	}
	defer func() { _ = flow.loopback.Close() }()

	code, err := flow.loopback.Wait(ctx, flow.pkce)
	if err != nil {
		return "", nil, err
	}
	cfg, err := auth.Config(flow.clientID)
	if err != nil {
		return "", nil, err
	}
	tokens, err := auth.Exchange(ctx, post, cfg, code, flow.pkce.Verifier, flow.loopback.RedirectURI())
	if err != nil {
		return "", nil, err
	}
	store, err = StoreTokenNotification(TokenKey(), tokens.AccessToken)
	if err != nil {
		return "", nil, err
	}
	return tokens.AccessToken, store, nil
}

// authBeginReply answers cascade.auth.begin.
func (p *broker) authBeginReply(in frame) (frame, bool) {
	var args struct {
		ClientID string `json:"client_id"`
	}
	if len(in.Params) > 0 {
		if err := json.Unmarshal(in.Params, &args); err != nil {
			return errorReply(in, err), true
		}
	}
	result, err := p.flows.begin(args.ClientID)
	if err != nil {
		return errorReply(in, err), true
	}
	return frame{JSONRPC: "2.0", ID: in.ID, Result: result}, true
}

// authCompleteReply answers cascade.auth.complete: it waits out the
// browser, stores the token, and reports only that it worked.
//
// The token itself is NOT in the reply. It goes to the vault through the
// host_secret_ref notification written to stdout just before this returns,
// and stays in this process's client for its own calls.
func (p *broker) authCompleteReply(in frame) (frame, bool) {
	token, store, err := p.flows.complete(context.Background(), p.post)
	if err != nil {
		return errorReply(in, err), true
	}
	if err := p.emit(store); err != nil {
		return errorReply(in, err), true
	}
	p.setToken(token)
	return frame{JSONRPC: "2.0", ID: in.ID, Result: map[string]any{
		"stored":   true,
		"key":      TokenKey(),
		"scope":    auth.RequiredScope,
		"verifier": nil, // stated explicitly: nothing secret is returned
	}}, true
}
