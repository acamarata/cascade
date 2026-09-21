// Purpose: mount the declared CLI commands of every PROCESS-runtime plugin
//
//	this binary ships, so a manifest's provides.commands entry is a verb a
//	user can actually type.
//
// WHY THIS FILE EXISTS. plugin_namespaces.go mounts BUILTIN plugins through
//
//	internal/plugins.BuiltinRegistry, which by construction serves
//	runtime="builtin" manifests only. cascade-github is runtime="process"
//	(plugins/github/manifest.toml), so NOTHING mounted its verbs: S-51.T1's
//	github-repos/github-issues/github-prs and S-51.T3's
//	github-ci-wait/github-ci-merge-on-green all existed in a manifest no
//	command tree read. `cascade github` was not a command at all.
//
// THE NESTING RULE (documented in .github/wiki/Plugin-Author-Guide.md).
//
//	pkg/plugin.CommandSpec has NO path or segments field -- Name,
//	Description, RPCMethod and nothing else -- so the nesting has to come
//	from the name. A declared command name's SEGMENTS ARE the command path:
//	  - "." separates segments when the name contains one, and a segment may
//	    then contain hyphens: `github.ci.merge-on-green` mounts as
//	    `cascade github ci merge-on-green`. This is the same namespacing
//	    convention provides.tools names already use in this schema.
//	  - "-" separates segments when the name contains no ".":
//	    `github-repos` mounts as `cascade github repos` and
//	    `github-ci-wait` as `cascade github ci wait`.
//	The hyphen form cannot express a verb that itself contains a hyphen
//	(`github-ci-merge-on-green` would mount five levels deep), which is
//	exactly why the dot form exists. The first segment is the plugin's
//	user-facing noun; it need not equal the manifest id (cascade-github's
//	noun is `github`).
//
// Inputs: the compiled-in spec set below, which a test holds byte-equal to
//
//	the real manifest, plus the host-implemented verb builders from
//	github_ci_cmd.go.
//
// Outputs: `cascade <noun> [...] <verb>` for each declared command.
//
// Constraints: the command tree MUST NOT depend on the environment
//
//	(mountConfigCmd's own note: an environment-dependent tree is a
//	golden-help test that flakes by machine). That is why the spec set is
//	COMPILED IN rather than scanned from the installed-plugin directory at
//	mount time, and why the drift between it and the shipped manifest is a
//	test rather than a runtime read. A derived noun that collides with a
//	core noun is never mounted over -- the plugin mounts under its manifest
//	id as a command that refuses with the reason, the same visible-refusal
//	choice plugin_namespaces.go's unloadablePluginCmd makes. That
//	guarantee covers the core nouns mounted AFTER this pass too; see
//	lateCoreNouns.
//
// SPORT: cmd/cascade:plugin-process-mount (ADD) -- P1-E25-W5-S51-T3.
package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// cascadeGitHubPluginID is the manifest id whose commands are mounted below.
const cascadeGitHubPluginID = "cascade-github"

// processPluginCommand is one process-runtime plugin's declared CLI verb.
type processPluginCommand struct {
	// PluginID is the declaring manifest's id.
	PluginID string
	// Spec is the manifest's own CommandSpec, verbatim.
	Spec plugin.CommandSpec
}

// processPluginCommands is the compiled-in mount set. Every entry is a
// verbatim copy of a provides.commands entry in the named manifest, and
// TestProcessPluginSpecsMatchTheShippedManifest fails if the two drift.
func processPluginCommands() []processPluginCommand {
	specs := []plugin.CommandSpec{
		{Name: "github-repos", Description: "List or inspect GitHub repositories."},
		{Name: "github-issues", Description: "List, open, comment on or close GitHub issues."},
		{Name: "github-prs", Description: "List, open, merge or request review on GitHub pull requests."},
		{
			Name: "github-ci-wait",
			Description: "Block until every required GitHub Actions check for a repo+ref reports success " +
				"(P1-E25-W5-S51-T3).",
		},
		{
			Name: "github.ci.merge-on-green",
			Description: "Merge a pull request once its required checks are green, gated by an explicit " +
				"merge-on-green policy grant (L3, P1-E25-W5-S51-T3).",
		},
	}
	out := make([]processPluginCommand, 0, len(specs))
	for _, s := range specs {
		out = append(out, processPluginCommand{PluginID: cascadeGitHubPluginID, Spec: s})
	}
	return out
}

// processPluginSpecs is the mount set mountProcessPluginCmds reads,
// indirected through a var for ONE reason: the collision guard below
// depends on the ORDER root.go mounts things in, so the test that proves
// it has to drive the REAL newRootCmd() with a hostile spec rather than a
// synthetic root of its own (a synthetic root is green for a reason
// production does not satisfy -- that is how the `ci` shadow below was
// missed). Production never assigns it.
var processPluginSpecs = processPluginCommands

// mountProcessPluginCmds is the production mount, called from
// mountPluginNamespaceCmds (root.go is at Art.10.3's 300-line cap).
func mountProcessPluginCmds(root *cobra.Command) {
	mountProcessPlugins(root, processPluginSpecs(), hostedProcessVerbs())
}

// processCommandSegments applies the nesting rule in this file's header.
// A name with an empty segment ("github--wait", "-wait", "github..ci")
// yields nil: it cannot name a command path, and guessing one would invent
// surface.
func processCommandSegments(name string) []string {
	trimmed := strings.TrimSpace(name)
	sep := "-"
	if strings.Contains(trimmed, ".") {
		sep = "."
	}
	segs := strings.Split(trimmed, sep)
	for _, s := range segs {
		if s == "" || strings.TrimSpace(s) != s {
			return nil
		}
	}
	return segs
}

// mountProcessPlugins attaches cmds to root. hosted maps a declared
// command name to a HOST-implemented builder; any command without one
// mounts the generic process-dispatch verb.
func mountProcessPlugins(root *cobra.Command, cmds []processPluginCommand, hosted map[string]func() *cobra.Command) {
	reserved := rootNouns(root)
	owned := map[string]*cobra.Command{}
	refused := map[string]bool{}
	for _, c := range cmds {
		segs := processCommandSegments(c.Spec.Name)
		switch {
		case len(segs) == 0:
			continue
		case reserved[segs[0]]:
			if !refused[c.PluginID] {
				root.AddCommand(collidingProcessPluginCmd(c.PluginID, segs[0]))
				refused[c.PluginID] = true
			}
		default:
			attachProcessVerb(root, owned, segs, c, hosted)
		}
	}
	for _, ns := range owned {
		guardUnknownSubcommands(ns)
	}
}

// lateCoreNouns are the CORE root nouns root.go's mountSubcommands adds
// AFTER it calls mountPluginNamespaceCmds -- that is, after this mount has
// already run. A snapshot of the live tree cannot see them, so they are
// reserved by name here instead.
//
// This is not hypothetical. mountCICmd is the LAST line of
// mountSubcommands, so before this list existed a plugin spec deriving the
// noun `ci` mounted a SECOND root `ci` command, Find(["ci","run"])
// resolved to the plugin's namespace, and `cascade ci run` became
// unreachable -- the exact opposite of what this file's header, the mount
// refusal and the Plugin Author Guide all promise. Reordering the mount is
// the other fix and is not available: root.go is at Art.10.3's 300-line
// cap. TestNoPluginCanShadowAnyCoreNoun drives the REAL newRootCmd() with
// a hostile spec for EVERY root noun the shipped tree carries, so the day
// another core mount lands after the plugin tier this list turns red
// rather than the CLI.
var lateCoreNouns = []string{"ci"}

// rootNouns is the reserved set: the command names root carries BEFORE
// this mount adds any (so a plugin noun is tested against the host's own
// nouns without also colliding with a namespace this mount just created),
// plus lateCoreNouns.
func rootNouns(root *cobra.Command) map[string]bool {
	out := map[string]bool{}
	for _, c := range root.Commands() {
		out[c.Name()] = true
	}
	for _, noun := range lateCoreNouns {
		out[noun] = true
	}
	return out
}

// attachProcessVerb walks segs, creating each intermediate namespace once,
// and attaches the leaf verb.
func attachProcessVerb(root *cobra.Command, owned map[string]*cobra.Command,
	segs []string, c processPluginCommand, hosted map[string]func() *cobra.Command,
) {
	parent, path := root, ""
	for _, seg := range segs[:len(segs)-1] {
		path += seg + " "
		next, ok := owned[path]
		if !ok {
			next = &cobra.Command{Use: seg, Short: processNamespaceShort(c.PluginID, path)}
			owned[path] = next
			parent.AddCommand(next)
		}
		parent = next
	}
	leaf := processLeafCmd(segs[len(segs)-1], c, hosted)
	guardUnknownSubcommands(leaf)
	parent.AddCommand(leaf)
}

// processLeafCmd builds one verb: the host implementation when this binary
// has one, else the generic process-dispatch verb.
func processLeafCmd(use string, c processPluginCommand, hosted map[string]func() *cobra.Command) *cobra.Command {
	if build, ok := hosted[c.Spec.Name]; ok {
		cmd := build()
		cmd.Use = use
		if cmd.Short == "" {
			cmd.Short = c.Spec.Description
		}
		return cmd
	}
	return &cobra.Command{
		Use:   use,
		Short: c.Spec.Description,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return errProcessPluginUnreachable(c.PluginID, cmd.CommandPath())
		},
	}
}

// processNamespaceShort labels an intermediate namespace. It names the
// declaring plugin, so `cascade --help` says where the noun came from
// rather than implying a host command.
func processNamespaceShort(pluginID, path string) string {
	trimmed := strings.TrimSpace(path)
	if !strings.Contains(trimmed, " ") {
		return "Commands contributed by the " + pluginID + " plugin"
	}
	return "`" + trimmed + "` commands contributed by the " + pluginID + " plugin"
}

// errProcessPluginUnreachable is the typed refusal a process-runtime verb
// returns while no process-tier plugin can be launched. It names the real
// blocker rather than reporting a generic failure: internal/plugins/
// dispatch.go's ProvisionElevated evaluates every process-tier install
// through process.ProcessRuntime.Launch's trust gate, which refuses
// TrustTierUntrusted, and no mechanism marks any manifest trusted yet.
func errProcessPluginUnreachable(pluginID, commandPath string) error {
	return cascade.Newf(cascade.KindUnavailable,
		"%s: the %s plugin is a process-tier plugin, and this build has no trust-elevation path that can "+
			"launch one, so there is no plugin process to dispatch this verb to (see internal/plugins/"+
			"dispatch.go's ProvisionElevated)", commandPath, pluginID)
}

// collidingProcessPluginCmd is what a plugin whose derived noun collides
// with a host noun mounts instead: visible in the help output, refusing
// with the reason. Shadowing the host noun, or silently dropping the
// plugin's verbs, are both worse.
func collidingProcessPluginCmd(pluginID, noun string) *cobra.Command {
	return &cobra.Command{
		Use:   pluginID,
		Short: "unavailable: the " + pluginID + " plugin's commands collide with the `" + noun + "` command",
		RunE: func(*cobra.Command, []string) error {
			return cascade.Newf(cascade.KindConflict,
				"cascade %s: this plugin's commands derive the noun %q, which is already a cascade command; "+
					"its verbs are not mounted. The plugin must rename them (see the nesting rule in "+
					"the Plugin Author Guide)", pluginID, noun)
		},
	}
}
