// Purpose: test fixtures for chat reply wiring tests.
// PORTABLE: no build tag, matches chat_reply_wiring_test.go.
package main

import (
	"strings"
	"sync"
	"testing"
)

// chatFixtureProvider tracks calls made during a reply.
type chatFixtureProvider struct {
	mu    sync.Mutex
	calls int
}

func (r *chatFixtureProvider) Call() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
}

func (r *chatFixtureProvider) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// chatFixturePolicy decides whether a call is allowed.
type chatFixturePolicy struct {
	ownerDenied bool
	quotaZero   bool
	localOnly   bool
}

func (c *chatFixturePolicy) Allow() bool {
	if c.ownerDenied || c.quotaZero || c.localOnly {
		return false
	}
	return true
}

func (c *chatFixturePolicy) Refusal() string {
	switch {
	case c.ownerDenied:
		return "refused: owner denied"
	case c.quotaZero:
		return "refused: quota exceeded"
	case c.localOnly:
		return "refused: local-only thread"
	default:
		return ""
	}
}

// chatFixtureLog records scrubbed egress payloads.
type chatFixtureLog struct {
	payloads []string
}

func (r *chatFixtureLog) Record(p string) {
	r.payloads = append(r.payloads, p)
}

func scrubPayload(p string) string {
	return strings.ReplaceAll(p, "vault:secret", "[REDACTED]")
}

// threadStore records persisted turns.
type threadStore struct {
	mu    sync.Mutex
	turns []string
}

func (s *threadStore) Persist(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = append(s.turns, t)
}

func (s *threadStore) TurnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.turns)
}

// rpcRegistry tracks registered methods.
type rpcRegistry struct {
	methods map[string]bool
}

func newRPCRegistry() *rpcRegistry {
	return &rpcRegistry{methods: make(map[string]bool)}
}

func (r *rpcRegistry) Register(name string) {
	r.methods[name] = true
}

func (r *rpcRegistry) Has(name string) bool {
	return r.methods[name]
}

// composedReplier simulates daemon composition with RecoverPending.
type composedReplier struct {
	provider  *chatFixtureProvider
	policy    *chatFixturePolicy
	log       *chatFixtureLog
	store     *threadStore
	registry  *rpcRegistry
	recovered bool
}

func newComposedReplier(
	p *chatFixtureProvider,
	pol *chatFixturePolicy,
	l *chatFixtureLog,
	s *threadStore,
	reg *rpcRegistry,
) *composedReplier {
	reg.Register("chat.reply_open")
	reg.Register("chat.reply")
	return &composedReplier{
		provider:  p,
		policy:    pol,
		log:       l,
		store:     s,
		registry:  reg,
		recovered: true,
	}
}

func (c *composedReplier) Reply(msg string) (string, error) {
	c.store.Persist("user: " + msg)
	if !c.policy.Allow() {
		refusal := c.policy.Refusal()
		c.store.Persist("assistant: " + refusal)
		return refusal, nil
	}
	c.provider.Call()
	scrubbed := scrubPayload(msg)
	c.log.Record(scrubbed)
	reply := "ok: " + scrubbed
	c.store.Persist("assistant: " + reply)
	return reply, nil
}

// assertCalls fails the test if provider calls do not match want.
func assertCalls(t *testing.T, p *chatFixtureProvider, want int) {
	t.Helper()
	if got := p.Calls(); got != want {
		t.Fatalf("provider calls = %d, want %d", got, want)
	}
}
