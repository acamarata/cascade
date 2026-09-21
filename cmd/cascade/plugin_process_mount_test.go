// Purpose: proves the process-runtime mount -- that the contracted verb
// paths exist in the BUILT cobra tree, that the compiled-in spec set cannot
// drift from the shipped manifest, that the nesting rule is what the Plugin
// Author Guide documents, and that a derived noun colliding with a core noun
// refuses instead of shadowing it.
//
// SPORT: cmd/cascade:plugin-process-mount (TESTED) -- P1-E25-W5-S51-T3.
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// findMounted resolves a command path in the real root tree.
func findMounted(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := newRootCmd().Find(path)
	if err != nil {
		t.Fatalf("Find(%v): %v", path, err)
	}
	if cmd.Name() != path[len(path)-1] {
		t.Fatalf("Find(%v) resolved to %q, so the leaf is NOT mounted", path, cmd.CommandPath())
	}
	return cmd
}

// TestProcessPluginMountBuildsTheContractedVerbPaths is the review's
// finding-3/contradiction-B answer: `cascade github ci wait` and
// `cascade github ci merge-on-green` exist in the shipped command tree.
func TestProcessPluginMountBuildsTheContractedVerbPaths(t *testing.T) {
	for _, path := range [][]string{
		{"github", "repos"},
		{"github", "issues"},
		{"github", "prs"},
		{"github", "ci", "wait"},
		{"github", "ci", "merge-on-green"},
		{"github", "wiki", "sync"},
		{"github", "wiki", "check"},
	} {
		cmd := findMounted(t, path...)
		if cmd.RunE == nil && cmd.Run == nil {
			t.Fatalf("%s has no Run: a mounted verb that does nothing is not mounted", cmd.CommandPath())
		}
		want := "cascade " + strings.Join(path, " ")
		if cmd.CommandPath() != want {
			t.Fatalf("CommandPath = %q, want %q", cmd.CommandPath(), want)
		}
	}
}

// TestProcessPluginSpecsMatchTheShippedManifest is the drift guard: the
// mount set is compiled in (the command tree must not depend on the
// environment), so the only thing keeping it honest is this comparison
// against the manifest the plugin actually ships.
func TestProcessPluginSpecsMatchTheShippedManifest(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "plugins", "github", "manifest.toml"))
	if err != nil {
		t.Fatalf("opening the shipped manifest: %v", err)
	}
	defer func() { _ = f.Close() }()
	manifest, err := plugin.ParseManifest(f)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Runtime != plugin.RuntimeProcess {
		t.Fatalf("manifest runtime = %q, want process (this mount serves the process tier)", manifest.Runtime)
	}

	var mounted []plugin.CommandSpec
	for _, c := range processPluginCommands() {
		if c.PluginID != manifest.ID {
			continue
		}
		mounted = append(mounted, c.Spec)
	}
	if !reflect.DeepEqual(mounted, manifest.Provides.Commands) {
		t.Fatalf("the compiled-in mount set has drifted from the manifest:\n mounted  = %+v\n manifest = %+v",
			mounted, manifest.Provides.Commands)
	}
}

// TestProcessCommandSegments pins the documented nesting rule.
func TestProcessCommandSegments(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"github-ci-wait", []string{"github", "ci", "wait"}},
		{"github-repos", []string{"github", "repos"}},
		{"github.ci.merge-on-green", []string{"github", "ci", "merge-on-green"}},
		{"github.repos", []string{"github", "repos"}},
		// The hyphen form CANNOT express a hyphenated leaf verb: five
		// segments, not three. This is why the dot form exists.
		{"github-ci-merge-on-green", []string{"github", "ci", "merge", "on", "green"}},
		{"github..ci", nil},
		{"github--wait", nil},
		{"-wait", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := processCommandSegments(c.name); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("processCommandSegments(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestProcessPluginMountRefusesACollidingNoun proves a plugin cannot shadow
// a core noun: `ci` is a host command, so a plugin declaring `ci-hijack`
// mounts a refusal under its own id and the core `ci` tree is untouched.
func TestProcessPluginMountRefusesACollidingNoun(t *testing.T) {
	root := &cobra.Command{Use: "cascade"}
	core := &cobra.Command{Use: "ci", Short: "the core CI noun"}
	root.AddCommand(core)

	mountProcessPlugins(root, []processPluginCommand{
		{PluginID: "evil-plugin", Spec: plugin.CommandSpec{Name: "ci-hijack"}},
		{PluginID: "evil-plugin", Spec: plugin.CommandSpec{Name: "ci-other"}},
	}, nil)

	if len(core.Commands()) != 0 {
		t.Fatalf("the core `ci` noun gained %d subcommands from a plugin", len(core.Commands()))
	}
	refusals := 0
	for _, c := range root.Commands() {
		if c.Name() == "evil-plugin" {
			refusals++
			if err := c.RunE(c, nil); !cascade.HasKind(err, cascade.KindConflict) {
				t.Fatalf("the colliding namespace's RunE = %v, want KindConflict", err)
			}
		}
	}
	if refusals != 1 {
		t.Fatalf("mounted %d refusal commands, want exactly 1 for the plugin", refusals)
	}
}

// withHostileSpec appends one extra spec to the PRODUCTION mount set for
// the duration of a test, so the assertion runs against the real
// newRootCmd() tree and its real mount ORDER.
func withHostileSpec(t *testing.T, pluginID, name string) {
	t.Helper()
	orig := processPluginSpecs
	t.Cleanup(func() { processPluginSpecs = orig })
	processPluginSpecs = func() []processPluginCommand {
		return append(orig(), processPluginCommand{PluginID: pluginID, Spec: plugin.CommandSpec{Name: name}})
	}
}

// countRootNoun reports how many root commands carry name.
func countRootNoun(root *cobra.Command, name string) int {
	n := 0
	for _, c := range root.Commands() {
		if c.Name() == name {
			n++
		}
	}
	return n
}

// TestACoreNounIsStillReachableUnderAHostilePlugin is the REAL-TREE
// version of the collision proof, and the regression test for the mount
// ORDER: the process tier mounts before mountCICmd, so a spec named
// `ci-hijack` used to mount a second root `ci` and make `cascade ci run`
// unreachable. Driven through newRootCmd(), not a synthetic root.
func TestACoreNounIsStillReachableUnderAHostilePlugin(t *testing.T) {
	withHostileSpec(t, "hostile-plugin", "ci-hijack")
	root := newRootCmd()

	if got := countRootNoun(root, "ci"); got != 1 {
		t.Fatalf("root carries %d commands named `ci`, want exactly the core one", got)
	}
	cmd, _, err := root.Find([]string{"ci", "run"})
	if err != nil {
		t.Fatalf("Find([ci run]): %v", err)
	}
	if cmd.CommandPath() != "cascade ci run" {
		t.Fatalf("`cascade ci run` resolved to %q: the plugin shadowed the core noun", cmd.CommandPath())
	}
	refusal, _, err := root.Find([]string{"hostile-plugin"})
	if err != nil || refusal.Name() != "hostile-plugin" {
		t.Fatalf("the colliding plugin mounted no refusal under its manifest id (got %v, %v)", refusal, err)
	}
	if err := refusal.RunE(refusal, nil); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("the refusal's RunE = %v, want KindConflict", err)
	}
}

// TestNoPluginCanShadowAnyCoreNoun generalizes the case above to EVERY
// noun the shipped tree carries, so a core mount added after the plugin
// tier (which lateCoreNouns must then name) turns this red.
func TestNoPluginCanShadowAnyCoreNoun(t *testing.T) {
	pluginOwned := map[string]bool{}
	for _, c := range processPluginCommands() {
		if segs := processCommandSegments(c.Spec.Name); len(segs) > 0 {
			pluginOwned[segs[0]] = true
		}
	}
	var nouns []string
	for _, c := range newRootCmd().Commands() {
		if !pluginOwned[c.Name()] {
			nouns = append(nouns, c.Name())
		}
	}
	if len(nouns) < 2 {
		t.Fatalf("only %d core nouns found; the tree was not built", len(nouns))
	}
	for _, noun := range nouns {
		t.Run(noun, func(t *testing.T) {
			withHostileSpec(t, "hostile-plugin", noun+".hijack")
			root := newRootCmd()
			if got := countRootNoun(root, noun); got != 1 {
				t.Fatalf("root carries %d commands named %q after a plugin derived it", got, noun)
			}
			if countRootNoun(root, "hostile-plugin") != 1 {
				t.Fatalf("noun %q: the plugin mounted no refusal under its manifest id", noun)
			}
		})
	}
}

// TestProcessPluginMountSkipsUnmountableNames proves a name that cannot
// yield a command path mounts nothing rather than inventing surface.
func TestProcessPluginMountSkipsUnmountableNames(t *testing.T) {
	root := &cobra.Command{Use: "cascade"}
	mountProcessPlugins(root, []processPluginCommand{
		{PluginID: "p", Spec: plugin.CommandSpec{Name: "--"}},
	}, nil)
	if len(root.Commands()) != 0 {
		t.Fatalf("root gained %d commands from an unmountable name", len(root.Commands()))
	}
}

// TestProcessVerbWithoutAHostImplementationRefuses proves the generic
// process-dispatch verb is honest: it names the launch path that does not
// exist rather than failing vaguely or pretending to succeed.
func TestProcessVerbWithoutAHostImplementationRefuses(t *testing.T) {
	_, err := execRoot(t, "github", "repos")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	for _, want := range []string{"cascade-github", "ProvisionElevated"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to name %q", err.Error(), want)
		}
	}
}
