package principal

// Purpose: mint, resolve and revoke memory-only caller credentials.
// Inputs: injected clock, principal and bounded TTL. Outputs: opaque token or fixed refusal.
// Constraints: no raw token is stored, logged or audited; restarts invalidate all tokens.
// SPORT: principal/Minter.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

var errUnrecognized = cascade.New(cascade.KindPermissionDenied, "caller principal not recognized")

// Minter holds only hashed credentials and their scopes. Configure Audit before serving.
type Minter struct {
	mu      sync.Mutex
	clock   runtime.Clock
	entries map[[32]byte]Principal
	revoked map[string]bool
	Audit   runtime.AuditRecorder
}

// NewMinter creates an independent token namespace for one daemon lifetime.
func NewMinter(clock runtime.Clock) *Minter {
	return &Minter{clock: clock, entries: make(map[[32]byte]Principal), revoked: make(map[string]bool)}
}

// Mint binds a fresh credential to p for at most one day.
func (m *Minter) Mint(p Principal, ttl time.Duration) (string, error) {
	if !mintable(p.Kind) || p.ID == "" || !projectScope(p.Scope) || ttl <= 0 || ttl > 24*time.Hour {
		return "", cascade.New(cascade.KindInvalidInput, "invalid caller principal")
	}
	if p.Kind == KindNodeJob && p.EnvelopeDigest == "" {
		return "", cascade.New(cascade.KindInvalidInput, "node job requires an envelope digest")
	}
	buf := make([]byte, 32)
	//nolint:forbidigo // crypto/rand, not math/rand: credentials require OS entropy.
	if _, err := rand.Read(buf); err != nil {
		return "", cascade.New(cascade.KindUnavailable, "caller principal entropy unavailable")
	}
	token := "cpt1_" + base64.RawURLEncoding.EncodeToString(buf)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revoked[p.ID] {
		return "", cascade.New(cascade.KindInvalidInput, "caller principal id revoked")
	}
	p.IssuedAt = m.clock.Now()
	p.ExpiresAt = p.IssuedAt.Add(ttl)
	m.entries[sha256.Sum256([]byte(token))] = p
	return token, nil
}

func mintable(k Kind) bool {
	switch k {
	case KindSession, KindMCP, KindHook, KindNodeJob, KindRemoteClient:
		return true
	case KindPlugin:
		return false
	default:
		return false
	}
}

// maxScopeLen matches the retrieval scope-ref bound.
const maxScopeLen = 512

// projectScope accepts only a resolved project ref "project:<id>": never
// personal, global or any other kind; the id is non-empty, not a wildcard,
// and free of whitespace and control characters, like a retrieval scope ref.
func projectScope(scope string) bool {
	id, ok := strings.CutPrefix(scope, "project:")
	if !ok || id == "" || len(scope) > maxScopeLen || strings.Contains(id, "*") {
		return false
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Resolve refuses all invalid credentials identically; only audit receives the reason.
func (m *Minter) Resolve(token string, now time.Time) (Principal, error) {
	m.mu.Lock()
	p, ok := m.entries[sha256.Sum256([]byte(token))]
	revoked := m.revoked[p.ID]
	m.mu.Unlock()
	reason := ""
	switch {
	case !ok:
		reason = "restart/unknown"
	case revoked:
		reason = "revoked"
	case !now.Before(p.ExpiresAt):
		reason = "expired"
	}
	if reason != "" {
		if m.Audit != nil {
			_ = m.Audit.Record(context.Background(), "caller_principal_refused", map[string]interface{}{"reason": reason})
		}
		return Principal{}, errUnrecognized
	}
	return p, nil
}

// Revoke permanently retires id in this daemon lifetime, including every token for it.
func (m *Minter) Revoke(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[id] = true
}

// Status reports live credentials. Access marking counts belong to the RPC registry.
func Status(m *Minter) (live int, marked int, unmarked int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.entries {
		if !m.revoked[p.ID] && m.clock.Now().Before(p.ExpiresAt) {
			live++
		}
	}
	return live, 0, 0
}
