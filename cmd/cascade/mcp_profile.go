// Purpose: resolves the MCP tool profile a serving process offers and
// answers the call-time capability checks that guard it.
// Inputs: the --profile flag value, the admitted [mcp].profile, and a
// daemon dialer bound to the process's own token.
// Outputs: resolveServeProfile, ProfileRaiseChecker, policyToolChecker,
// profileToolVisible, profileToolNames and executiveServeConfig.
// Constraints: fail closed. A flag never widens past the admitted profile
// unless the daemon answers allow on mcp.profile.<p> for the process's own
// principal; a nil checker, a deny, an ask, an RPC error or an unreachable
// daemon all keep the admitted profile. The subject is never read from the
// flag, tool arguments, clientInfo or the environment.
// SPORT: cmd/cascade mcp profile resolution (P1-HRN-05).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// profileCheckVerb is the daemon verb both checkers dial.
const profileCheckVerb = "mcp.capability.check"

// ProfileRaiseChecker asks the daemon whether the process's own principal
// holds mcp.profile.<p>. A nil error is an allow verdict.
type ProfileRaiseChecker func(ctx context.Context, p mcp.ToolProfile) error

// checkMCPCapability asks the daemon verb mcp.capability.check whether the
// caller bound into c holds capability. Anything short of allow is an error.
func checkMCPCapability(ctx context.Context, c *client.Client, capability string) error {
	if c == nil {
		return cascade.New(cascade.KindUnavailable, "mcp: daemon dialer returned no client")
	}
	var res policy.CheckResult
	params := struct {
		Capability string `json:"capability"`
	}{capability}
	if err := c.Do(ctx, profileCheckVerb, params, &res); err != nil {
		return err
	}
	if res.Verdict != policy.VerdictAllow.String() {
		return cascade.ErrPolicyDenied
	}
	return nil
}

// newProfileRaiseChecker binds a raise checker to the process token. With no
// token (or the empty-token marker) it returns nil, which never raises.
func newProfileRaiseChecker(getenv func(string) string, dial func(token string) (*client.Client, error)) ProfileRaiseChecker {
	if getenv == nil || dial == nil {
		return nil
	}
	token := getenv(principal.EnvToken)
	if token == "" || token == emptyTokenMarker {
		return nil
	}
	return func(ctx context.Context, p mcp.ToolProfile) error {
		c, err := dial(token)
		if err != nil {
			return err
		}
		return checkMCPCapability(ctx, c, "mcp.profile."+string(p))
	}
}

// policyToolChecker returns the call-time checker for compact-write tools.
// Every call asks the daemon about mcp.tool.<name> for the caller bound into
// the dialled client; a verdict other than allow, an RPC error or an
// unreachable daemon refuses.
func policyToolChecker(dial func() (*client.Client, error)) mcp.ToolCapabilityChecker {
	return daemonToolChecker{dial: dial}
}

type daemonToolChecker struct {
	dial func() (*client.Client, error)
}

// CheckTool implements mcp.ToolCapabilityChecker.
func (d daemonToolChecker) CheckTool(ctx context.Context, tool string) error {
	if d.dial == nil {
		return cascade.ErrPolicyDenied
	}
	c, err := d.dial()
	if err != nil {
		return err
	}
	return checkMCPCapability(ctx, c, mcp.ToolCapability(tool))
}

// resolveServeProfile returns min(flag, admitted): lowering is free, and a
// wider flag is honoured only when raise answers allow. The second result is
// the one stderr note printed when the flag was refused.
func resolveServeProfile(ctx context.Context, flag string, eff *runtime.EffectiveConfig, raise ProfileRaiseChecker) (mcp.ToolProfile, string) {
	admitted := admittedMCPProfile(eff)
	if flag == "" {
		return admitted, ""
	}
	want := mcp.ParseToolProfile(flag)
	if mcpProfileRank(want) <= mcpProfileRank(admitted) {
		return want, ""
	}
	if raise != nil && raise(ctx, want) == nil {
		return want, ""
	}
	return admitted, fmt.Sprintf("cascade: --profile %s exceeds the admitted [mcp].profile %s; serving %s", want, admitted, admitted)
}

// profileToolNames returns the tool names profile p exposes, with each
// compact alias resolved. nil means no member filter (the full profile).
func profileToolNames(p mcp.ToolProfile) []string {
	members := mcp.ProfileMembers(p)
	if members == nil {
		return nil
	}
	if mcp.ParseToolProfile(string(p)) != mcp.ProfileFull {
		for alias, target := range mcp.CompactAlias {
			for _, m := range members {
				if m == target {
					members = append(members, alias)
					break
				}
			}
		}
	}
	return members
}

// profileToolVisible reports whether tool (a member name or a compact
// alias) is offered under profile p.
func profileToolVisible(p mcp.ToolProfile, tool string) bool {
	names := profileToolNames(p)
	if names == nil {
		return true
	}
	for _, n := range names {
		if n == tool {
			return true
		}
	}
	return false
}

// executiveServeConfig returns the MCP client configuration for a session
// that serves the compact-write profile from the absolute binary path bin.
// The session still gets compact-write only through resolveServeProfile.
func executiveServeConfig(bin string) ([]byte, error) {
	return mcp.ExecutiveMCPConfigJSON(bin)
}

// serveProfileState is what `mcp serve` resolved before serving: the profile,
// the one stderr note when the flag was refused, and the call-time checker.
type serveProfileState struct {
	Profile mcp.ToolProfile
	Note    string
	Checker mcp.ToolCapabilityChecker
}

type serveProfileKey struct{}

// productionServeProfile resolves the profile from the on-disk [mcp]
// section (process environment overrides are not read, so no variable can
// widen it) and binds the raise and tool checkers to the process token only.
func productionServeProfile(paths runtime.PathProvider, lookup func(string) (string, bool)) func(context.Context, string) serveProfileState {
	return func(ctx context.Context, flag string) serveProfileState {
		var eff *runtime.EffectiveConfig
		if paths != nil {
			cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath(),
				Environ: func() []string { return nil }})
			if err == nil {
				e := runtime.EffectiveConfigOf(cfg)
				eff = &e
			}
		}
		getenv := mcpPrincipalGetenv(lookup)
		dial := dialMCPDaemon(paths)
		st := serveProfileState{}
		st.Profile, st.Note = resolveServeProfile(ctx, flag, eff, newProfileRaiseChecker(getenv, dial))
		if token := getenv(principal.EnvToken); token != "" && token != emptyTokenMarker {
			st.Checker = policyToolChecker(func() (*client.Client, error) { return dial(token) })
		}
		return st
	}
}

// serveContext resolves the serve profile for cmd and returns ctx carrying
// it. Without a resolver the admitted default (compact) is served.
func serveContext(ctx context.Context, stderr io.Writer, resolve func(context.Context, string) serveProfileState, flag string) context.Context {
	st := serveProfileState{Profile: mcp.ProfileCompact}
	if resolve != nil {
		st = resolve(ctx, flag)
	} else {
		st.Profile, st.Note = resolveServeProfile(ctx, flag, nil, nil)
	}
	if st.Note != "" {
		_, _ = fmt.Fprintln(stderr, st.Note)
	}
	return context.WithValue(ctx, serveProfileKey{}, st)
}

// profileServer returns the dispatcher serving srv under the profile in ctx.
// With no resolved profile the server is returned unchanged.
func profileServer(ctx context.Context, srv *mcp.Server) transport.Dispatcher {
	st, ok := ctx.Value(serveProfileKey{}).(serveProfileState)
	if !ok {
		return srv
	}
	srv.WithToolChecker(st.Profile, st.Checker)
	return profileDispatcher{inner: srv, profile: st.Profile}
}

type profileDispatcher struct {
	inner   *mcp.Server
	profile mcp.ToolProfile
}

// Dispatch hides tools outside the profile from tools/list and refuses them
// on tools/call exactly as an unknown tool.
func (d profileDispatcher) Dispatch(ctx context.Context, f *mcp.Frame) *mcp.Response {
	switch f.Method {
	case mcp.MethodToolsList:
		return d.filterList(d.inner.Dispatch(ctx, f))
	case mcp.MethodToolsCall:
		var p map[string]json.RawMessage
		var name string
		if json.Unmarshal(f.Params, &p) == nil && json.Unmarshal(p["name"], &name) == nil {
			if target, ok := mcp.CompactAlias[name]; ok && d.profile != mcp.ProfileFull {
				name = target
				p["name"], _ = json.Marshal(name)
				f.Params, _ = json.Marshal(p)
			}
			if !profileToolVisible(d.profile, name) {
				return &mcp.Response{JSONRPC: "2.0", ID: f.ID,
					Error: &mcp.ErrorObject{Code: -32001, Message: "unknown tool: " + name}}
			}
		}
	}
	return d.inner.Dispatch(ctx, f)
}

func (d profileDispatcher) filterList(resp *mcp.Response) *mcp.Response {
	if resp == nil || resp.Error != nil {
		return resp
	}
	raw, err := json.Marshal(resp.Result)
	var res struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err != nil || json.Unmarshal(raw, &res) != nil {
		return &mcp.Response{JSONRPC: "2.0", ID: resp.ID,
			Error: &mcp.ErrorObject{Code: -32603, Message: "mcp: tools/list is not filterable"}}
	}
	kept := make([]json.RawMessage, 0, len(res.Tools))
	for _, t := range res.Tools {
		var n struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(t, &n) == nil && profileToolVisible(d.profile, n.Name) {
			kept = append(kept, t)
		}
	}
	out := *resp
	out.Result = map[string]any{"tools": kept}
	return &out
}
