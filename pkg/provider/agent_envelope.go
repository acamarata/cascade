package provider

// Purpose: SpawnEnvelope, the driver-facing projection of the one scope
//
//	envelope (contract:scope-envelope, ADR-0007), and its fail-closed
//	completeness check. An external agent driver calls Check before any
//	child exists; an incomplete envelope never reaches a spawn.
//
// Inputs: the projection fields (internal/providers/agents.ProjectEnvelope is
//
//	the only production writer), the job's worktree root and the current time.
//
// Outputs: nil, or ErrEnvelopeIncomplete wrapped with the failing field.
// Constraints: this is not a second envelope model: every field is copied from
//
//	a verified pkg/envelope.Envelope, and pkg/provider never imports
//	pkg/envelope (pkg/envelope imports pkg/provider). Check reads no clock:
//	the caller passes now. Roots are absolute, clean, slash-separated.
//
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-AGT-02).

import (
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrEnvelopeIncomplete is the sentinel every Check refusal wraps.
var ErrEnvelopeIncomplete = cascade.New(cascade.KindPolicyDenied, "agent: spawn envelope incomplete")

// digestHexLen is the length of an envelope.Digest (hex of sha256).
const digestHexLen = 64

// SpawnEnvelope is the projection of a verified scope envelope an external
// driver needs before it may start a child.
type SpawnEnvelope struct {
	// Digest is envelope.Digest of the verified envelope: 64 lowercase hex.
	Digest string
	// Principal is the caller-principal ID, never a token.
	Principal string
	// Roots are the absolute, clean, slash-separated filesystem roots.
	Roots []string
	// Tools, Commands and Egress are the allowed names and egress classes.
	Tools, Commands, Egress []string
	// Sensitivity is the envelope's data tier.
	Sensitivity SensitivityTier
	// ExpiresAt is the end of the validity window (exclusive).
	ExpiresAt time.Time
}

// Check reports ErrEnvelopeIncomplete, wrapped with the failing field, when
// e is not usable for a spawn in worktreeRoot at now: a Digest that is not 64
// lowercase hex; an empty Principal; empty Roots or a root that is not
// absolute and clean; a zero or not-future ExpiresAt; a worktreeRoot that is
// not absolute and clean, or is neither a root nor under one at a path
// segment boundary. A nil return means every rule held.
func (e SpawnEnvelope) Check(worktreeRoot string, now time.Time) error {
	if !isLowerHex(e.Digest, digestHexLen) {
		return incomplete("Digest is not 64 lowercase hex")
	}
	if e.Principal == "" {
		return incomplete("Principal is empty")
	}
	if len(e.Roots) == 0 {
		return incomplete("Roots is empty")
	}
	for _, r := range e.Roots {
		if !absClean(r) {
			return incomplete("Roots holds a root that is not absolute and clean")
		}
	}
	if e.ExpiresAt.IsZero() || !e.ExpiresAt.After(now) {
		return incomplete("ExpiresAt is zero or not after now")
	}
	if !absClean(worktreeRoot) || !e.covers(worktreeRoot) {
		return incomplete("worktree root is not under any envelope root")
	}
	return nil
}

// covers reports whether dir (slash form, absolute, clean) equals a root or
// lies beneath one at a path-segment boundary.
func (e SpawnEnvelope) covers(dir string) bool {
	dir = filepath.ToSlash(dir)
	for _, r := range e.Roots {
		r = strings.TrimSuffix(filepath.ToSlash(rootPath(r)), "/")
		if dir == r || strings.HasPrefix(dir, r+"/") {
			return true
		}
	}
	return false
}

// incomplete wraps ErrEnvelopeIncomplete with the failing field's detail.
func incomplete(what string) error {
	return cascade.Wrap(cascade.KindPolicyDenied, ErrEnvelopeIncomplete, "agent: spawn envelope incomplete: "+what)
}

// rootPath maps a root as pkg/envelope spells it to a native path: on Windows
// "/C:/x" is C:\x (an envelope root always starts with "/"); elsewhere the
// slash form is the native form.
func rootPath(p string) string {
	if runtime.GOOS == "windows" && len(p) > 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// absClean reports whether p is absolute and already in cleaned form (the
// slash spelling of a Windows path counts as its native form).
func absClean(p string) bool {
	native := rootPath(p)
	return p != "" && filepath.IsAbs(native) && filepath.Clean(native) == native
}

// isLowerHex reports whether s is exactly n characters of [0-9a-f].
func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
