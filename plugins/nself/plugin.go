// Package nself is the cascade-nself builtin plugin: nself-project
// DETECTION (detect.go), a doctor probe (doctor.go), and one tool that
// refuses, typed, until the nself CLI and the host each grow a verb this
// plugin cannot supply itself.
//
// THIS IS THE HONEST FLOOR, AND IT IS SMALLER THAN THE CONTRACT. Both
// sides, quoted:
//
//   - The contract's HANDSHAKE section names `nself add cascade` and its
//     DETECTION section names `nself project status --json`. Probed
//     read-only against the nself actually installed on the build machine
//     (v1.3.5): `nself add <name>` is the short form of `nself plugin
//     install <name>` — a registry plugin installer that emits no JSON and
//     no server-profile fields — and there is no `project` verb at all
//     (`unknown command "project"`). The one project-scoped verb with a
//     real JSON mode is `nself status --json` (`nself status --help`
//     confirms `-j, --json`), which this plugin's probe uses instead.
//     Building a parser, fixture and fuzz corpus for a response shape
//     nobody emits would be a dialect this package invented for itself,
//     which Art.2 forbids; the draft this replaces did exactly that and
//     the adversarial review rejected it.
//   - The contract's SOFT-DEFAULT section writes the handshake's fields
//     through "C-S05.T8's config write verbs". internal/runtime's
//     config_write.go is dotted-path VALIDATION only (SplitDottedPath /
//     ResolveDottedPath); no diff-apply seam exists anywhere in the tree.
//
// So nself_add_cascade returns ONE typed refusal naming both missing
// prerequisites, and this package contains no handshake protocol, no
// fixture of an uncaptured response, and no config-apply path. The
// planning contradictions are filed (PCI s52t2-nself-barred-by-identifier
// -sweep, items B and C).
//
// RUNTIME TIER (T0 ruling, PCI item A): the contract says `runtime =
// process, trust_tier = trusted`. pkg/plugin.Manifest has no trust_tier or
// net_scopes field, internal/plugins/dispatch.go's ProvisionElevated
// refuses every process-tier install (no code path marks any manifest
// trusted), and internal/plugins/builtin_tier_only_test.go asserts every
// compile-time registration is RuntimeBuiltin. RuntimeBuiltin is therefore
// the floor, and it IS a security downgrade that is stated rather than
// glossed: this plugin runs IN the daemon process with full host trust and
// no supervised child. What it forks itself (one bounded `nself status
// --json`) is an ordinary subprocess, bounded by a deadline and a process-
// group kill, not a host-launched process-tier plugin.
//
// REACHABILITY: internal/plugins/nself_wiring.go imports this package and
// binds the real egress engine, so cascade-nself reaches plugin.Builtins()
// in the shipped binary (internal/plugins/registry_test.go's pinned
// inventory is what proves it). With no interceptor bound, every tool
// response refuses to emit rather than passing through unfiltered.
//
// SPORT: plugins/nself entity (ADD) — P1-E25-W5-S52-T2.
package nself

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginID is this plugin's manifest id.
const pluginID = "cascade-nself"

// toolProjectInfo and toolAddCascade are the two tool names the contract's
// MANIFEST section names verbatim (bare, not cascade-nself.-prefixed).
const (
	toolProjectInfo = "nself_project_info"
	toolAddCascade  = "nself_add_cascade"
)

// init registers cascade-nself with the host's compile-time registry.
func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest returns this plugin's cascade.plugin/v2 manifest, kept
// consistent with manifest.toml (plugin_test.go asserts this through the
// real loader).
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "Cascade nSelf",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Tools: []plugin.ToolSpec{
				{Name: toolProjectInfo, Description: "Report whether a directory is an nself-managed project, and whether the nself CLI is reachable."},
				{Name: toolAddCascade, Description: "Refuses: the nself handshake verb and the host config-apply seam this would need do not exist yet."},
			},
		},
		Requires: []string{"subprocess_exec"},
		Permissions: []plugin.PermissionDisplay{
			{Name: "subprocess_exec", Description: "Run one bounded, read-only nself status probe as a local subprocess in the scanned directory."},
		},
	}
}

// handlers is the real plugin.BuiltinHandlers implementation.
type handlers struct{}

func (handlers) DispatchTool(ctx context.Context, name string, input []byte) ([]byte, error) {
	switch name {
	case toolProjectInfo:
		return dispatchProjectInfo(ctx, input)
	case toolAddCascade:
		return nil, addCascadeRefusal()
	default:
		return nil, cascade.Newf(cascade.KindNotFound, "cascade-nself: no such tool %q", name)
	}
}

func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindNotFound,
		"cascade-nself: no such intent %q: this plugin declares no intents", name)
}

func (handlers) RunCommand(_ context.Context, name string, _ []string) error {
	return cascade.Newf(cascade.KindNotFound,
		"cascade-nself: unknown command %q: this plugin declares no commands", name)
}

// errAddCascadeUnavailable is the refusal identity nself_add_cascade
// carries. It is a plain sentinel, not a cascade.Error, precisely so a
// test can assert THIS error rather than "some KindUnsupported error":
// cascade.Error's errors.Is compares Kind only.
var errAddCascadeUnavailable = errors.New(
	"cascade-nself: nself_add_cascade is unavailable: the installed nself CLI has no `add cascade` handshake verb " +
		"that emits a server profile (v1.3.5: `nself add` installs a registry plugin), and this host has no " +
		"config diff-apply seam to write one through (internal/runtime's config_write is dotted-path validation only)")

// addCascadeRefusal is the ONE error this tool ever returns.
func addCascadeRefusal() error {
	return cascade.Wrap(cascade.KindUnsupported, errAddCascadeUnavailable,
		"cascade-nself: both prerequisites are missing, so nothing is attempted")
}

// toolInput is the optional JSON body nself_project_info accepts: an
// override root directory for the detection scan, for a caller that is not
// operating from the daemon's own working directory.
type toolInput struct {
	RootDir string `json:"root_dir"`
}

// parseToolInput decodes input, treating an unusable body as "no override"
// rather than an error: a detection question with a malformed body is
// still answerable about the working directory. It never panics
// (FuzzNselfToolInput).
func parseToolInput(input []byte) toolInput {
	var t toolInput
	if len(input) == 0 {
		return t
	}
	if err := json.Unmarshal(input, &t); err != nil {
		return toolInput{}
	}
	return t
}

// getwd resolves the daemon's working directory when no override is given.
// A package var so plugin_test.go can drive the failure branch.
var getwd = os.Getwd

// rootDirOf resolves the directory to scan.
func rootDirOf(t toolInput) string {
	if t.RootDir != "" {
		return t.RootDir
	}
	wd, err := getwd()
	if err != nil {
		return "."
	}
	return wd
}

// dispatchProjectInfo answers the detection question. It returns an error
// only when the firewall refuses to pass the response — never because the
// scanned directory turned out not to be an nself project.
func dispatchProjectInfo(ctx context.Context, input []byte) ([]byte, error) {
	root := rootDirOf(parseToolInput(input))
	res := newDetector(root).Detect(ctx)
	dr, derr := runDoctor(activeLocator, nselfBinary)
	return marshalThroughEgress(ctx, newProjectInfoResponse(res, dr, derr).scrubbed())
}

// marshalThroughEgress JSON-encodes v and passes the bytes through the
// active EgressInterceptor at TierInternal. With no interceptor bound this
// REFUSES; with the real one bound (internal/plugins/nself_wiring.go) the
// substitution and sensitivity passes run on every byte this plugin emits.
func marshalThroughEgress(ctx context.Context, v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "cascade-nself: encode tool response")
	}
	return egressInterceptor.InterceptClass(ctx, EgressClassNselfBackend, TierInternal, data)
}
