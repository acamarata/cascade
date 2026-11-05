// Package plugins (installed_tools.go): Purpose: offer the MCP tools of the
// installed first-party process plugins to mcp.NewToolRegistryWithProcess,
// and route each call through LaunchInstalled.
// Inputs: the LaunchEnv source (launch_env.go), the stored record of each
// embedded first-party manifest (first_party_manifests.go), the executable
// beside the cascade binary (process_exec.go).
// Outputs: InstalledToolSource.
// Constraints: a plugin is offered only when its record is an enabled,
// trusted process record whose resolved executable still hashes to the
// pinned ExecDigest; its Grants are EffectiveGrants(rec), never the
// manifest's Requires. Every call re-loads the record and re-runs the
// exposure rule (refusal: KindPolicyDenied), then launches (refusal:
// KindUnavailable), calls "<tool>" on the handle and closes it. ctx passes
// through unchanged, so a WithParamsDigest override reaches the handle.
// Every result the plugin returns, a typed error reply included, carries
// _meta {"cascade/provenance": "untrusted-source"}.
// SPORT: internal/plugins installed process tools (P1-PLG-06).
package plugins

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// ProvenanceMetaKey is the _meta key a process-tool result's provenance
// travels under.
const ProvenanceMetaKey = "cascade/provenance"

// InstalledToolSource returns the production process-tool source. Each
// call resolves the launch environment afresh; no environment, a load
// error or a refused record contributes nothing.
func InstalledToolSource() mcp.ProcessToolSource {
	return func() []mcp.ProcessToolRegistration {
		ctx := context.Background()
		env, err := ResolveLaunchEnv(ctx)
		if err != nil {
			return nil
		}
		var out []mcp.ProcessToolRegistration
		for _, fp := range FirstPartyManifests() {
			grants, err := exposedGrants(ctx, env, fp.Manifest.ID)
			if err != nil {
				continue
			}
			out = append(out, mcp.ProcessToolRegistration{Manifest: fp.Manifest, Grants: grants, Call: installedCall(fp.Manifest)})
		}
		return out
	}
}

// exposedGrants returns EffectiveGrants of id's record when it is an
// enabled, trusted process record whose executable still matches its
// pinned digest; otherwise the refusal.
func exposedGrants(ctx context.Context, env LaunchEnv, id string) ([]string, error) {
	rec, err := trustedRecord(ctx, env, id)
	if err != nil {
		return nil, err
	}
	path, err := ResolveExecutable(id)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindPolicyDenied, err, "plugins: %s: its tools are not exposed", id)
	}
	digest, err := ExecutableDigest(path)
	if err != nil || digest != rec.ExecDigest {
		return nil, cascade.Newf(cascade.KindPolicyDenied,
			"plugins: %s: executable changed since it was trusted; run `%s`", id, ReverifyCommand(id))
	}
	return EffectiveGrants(rec), nil
}

// installedCall is m's registration Call: re-check, launch, call, close.
func installedCall(m plugin.Manifest) func(ctx context.Context, tool string, input []byte) ([]byte, error) {
	return func(ctx context.Context, tool string, input []byte) ([]byte, error) {
		if err := recheckExposure(ctx, m); err != nil {
			return nil, err
		}
		h, err := LaunchInstalled(ctx, m.ID)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "plugins: %s: tool %s: the plugin could not be launched", m.ID, tool)
		}
		defer func() { _ = h.Close(context.WithoutCancel(ctx)) }()
		out, err := h.Call(ctx, tool, input)
		if err != nil && !cascade.HasKind(err, cascade.KindInternal) {
			return nil, err
		}
		return untrustedResult(out, err)
	}
}

// recheckExposure re-loads m's record and re-runs the exposure rule; any
// failure is KindPolicyDenied and nothing is launched.
func recheckExposure(ctx context.Context, m plugin.Manifest) error {
	env, err := ResolveLaunchEnv(ctx)
	if err != nil {
		return cascade.Wrapf(cascade.KindPolicyDenied, err, "plugins: %s: its tools are not exposed", m.ID)
	}
	grants, err := exposedGrants(ctx, env, m.ID)
	if err != nil {
		return cascade.Wrapf(cascade.KindPolicyDenied, err, "plugins: %s: its tools are not exposed", m.ID)
	}
	reg := mcp.ProcessToolRegistration{Manifest: m, Grants: grants, Call: func(context.Context, string, []byte) ([]byte, error) { return nil, nil }}
	if !mcp.ProcessExposable(reg) {
		return cascade.Newf(cascade.KindPolicyDenied,
			"plugins: %s: its grants no longer equal what its manifest requires; its tools are not exposed", m.ID)
	}
	return nil
}

// untrustedEnvelope is a process tool's MCP result: the plugin's result or
// its typed error reply, marked as untrusted-source content.
type untrustedEnvelope struct {
	Result json.RawMessage   `json:"result,omitempty"`
	Error  string            `json:"error,omitempty"`
	Meta   map[string]string `json:"_meta"`
}

// untrustedResult wraps the plugin's reply (out, or its typed error reply
// replyErr) in the provenance envelope.
func untrustedResult(out []byte, replyErr error) ([]byte, error) {
	env := untrustedEnvelope{Meta: map[string]string{ProvenanceMetaKey: string(provider.ProvenanceUntrustedSource)}}
	switch {
	case replyErr != nil:
		env.Error = replyErr.Error()
	case len(out) > 0 && json.Valid(out):
		env.Result = json.RawMessage(out)
	case len(out) > 0:
		quoted, _ := json.Marshal(string(out))
		env.Result = quoted
	}
	return json.Marshal(env)
}
