// Package principal owns process-local caller credentials.
package principal

import (
	"context"
	"strings"
	"time"
)

// Kind identifies the launch authority associated with a caller.
type Kind string

// Caller kinds identify the five mintable authorities and the reserved plugin kind.
const (
	KindSession      Kind = "session"
	KindMCP          Kind = "mcp"
	KindHook         Kind = "hook"
	KindNodeJob      Kind = "node-job"
	KindRemoteClient Kind = "remote-client"
	// KindPlugin is reserved and cannot be minted.
	KindPlugin Kind = "plugin"
)

// Principal is the immutable scope bound to a daemon-issued token.
type Principal struct {
	ID              string
	Kind            Kind
	SessionID       string
	ParentSessionID string
	Scope           string
	EnvelopeDigest  string
	IssuedAt        time.Time
	ExpiresAt       time.Time
}

// Caller carries transport-verified identity. OwnerCLI never accompanies a principal.
type Caller struct {
	Principal *Principal
	OwnerCLI  bool
}

type callerKey struct{}

// WithCaller binds a copy, preventing subsequent mutation by the caller.
func WithCaller(ctx context.Context, c Caller) context.Context {
	if c.Principal != nil {
		p := *c.Principal
		c.Principal, c.OwnerCLI = &p, false
	}
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom retrieves the transport identity; absence is never an owner caller.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	if c.Principal != nil {
		p := *c.Principal
		c.Principal = &p
	}
	return c, ok
}

// Credential transport names are shared by launchers, the SDK and the socket handler.
const (
	EnvToken       = "CASCADE_PRINCIPAL_TOKEN"
	HeaderToken    = "Cascade-Principal"
	HeaderOwnerCLI = "Cascade-Client"
)

// BindEnv replaces all inherited credentials without mutating env.
func BindEnv(env []string, token string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, EnvToken+"=") {
			out = append(out, entry)
		}
	}
	return append(out, EnvToken+"="+token)
}
