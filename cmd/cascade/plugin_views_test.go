package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): cover the plugin command views. These are the exact
//   strings an operator reads back after an install, a permission change or
//   an update, and none of them had a test — so nothing stopped a rename or
//   a dropped column from shipping silently.
// SPORT: cmd/cascade plugin-view tests (ADD).

// TestPluginListView_EmptyAndPopulated asserts both the empty case (which
// must read as a sentence, not as a bare header with no rows) and the
// populated one.
func TestPluginListView_EmptyAndPopulated(t *testing.T) {
	if got := (pluginListView{}).String(); got != "this build ships no plugins and none are installed" {
		t.Fatalf("empty list rendered %q, want a plain-language sentence", got)
	}

	// BOTH populations, because the W-4 hardening gate found `plugin
	// list` blind to the ones this binary ships: `cascade init` named
	// five plugins and this verb said none were installed, while their
	// commands worked (R-14.277).
	got := pluginListView{
		Installed: []plugins.PluginMetadata{
			{Name: "other", InstalledVersion: "2.3.4", Enabled: false, RuntimeMode: "process"},
		},
		Builtin: []builtinPluginRow{
			{Name: "cascade-claude", Version: "0.1.0", Runtime: "builtin"},
		},
	}.String()

	for _, want := range []string{
		"NAME", "SOURCE", "VERSION", "ENABLED", "RUNTIME",
		"cascade-claude", "0.1.0", "builtin",
		"other", "2.3.4", "false", "process", "installed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered list is missing %q:\n%s", want, got)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("rendered list ends with a newline; the writer adds its own")
	}
	if lines := strings.Count(got, "\n") + 1; lines != 3 {
		t.Errorf("rendered list has %d lines, want a header plus one row per plugin", lines)
	}
}

// TestBuiltinsAreNotToggleable pins the truthful refusal. Saying a builtin
// was "not installed" was actionable and wrong: an operator could go and
// try to install a plugin whose commands were already working.
func TestBuiltinsAreNotToggleable(t *testing.T) {
	err := errBuiltinNotToggleable("disable", "pbd")
	if err == nil {
		t.Fatal("a builtin toggle was permitted")
	}
	for _, want := range []string{"built into this binary", "always active", "pbd"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "not installed") {
		t.Errorf("the refusal still claims the plugin is not installed: %v", err)
	}
}

// TestTheBuiltinRegistryIsReadable proves this binary really does ship
// plugins, so the list above is not a view over an empty set forever.
func TestTheBuiltinRegistryIsReadable(t *testing.T) {
	rows, err := builtinPluginRows()
	if err != nil {
		t.Fatalf("the builtin registry would not load: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("this build registers no builtin plugins; `cascade init` would list none either")
	}
	for _, r := range rows {
		if r.Name == "" || r.Version == "" {
			t.Errorf("builtin row %+v is missing its identity", r)
		}
		if !isBuiltinPlugin(r.Name) {
			t.Errorf("%q is listed as a builtin but isBuiltinPlugin says otherwise", r.Name)
		}
	}
	if isBuiltinPlugin("definitely-not-a-builtin") {
		t.Error("an unknown name reports as a builtin; every plugin would be untoggleable")
	}
}

// TestPluginInfoView_RendersEveryField pins the whole record. A field
// dropped from this view is a field an operator silently stops being able
// to see.
func TestPluginInfoView_RendersEveryField(t *testing.T) {
	got := pluginInfoView(plugins.PluginMetadata{
		Name:             "cascade-claude",
		InstalledVersion: "0.1.0",
		Enabled:          true,
		RuntimeMode:      "builtin",
		Grants:           []string{"read-context", "hook-emit"},
	}).String()

	for _, want := range []string{
		"FIELD", "VALUE",
		"name", "cascade-claude",
		"version", "0.1.0",
		"enabled", "true",
		"runtime", "builtin",
		"grants", "read-context,hook-emit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered record is missing %q:\n%s", want, got)
		}
	}
}

// TestPluginInfoViewShowsAnEmptyGrantSet proves a plugin with no grants
// still renders the row, rather than omitting it and leaving the reader
// unsure whether the field is empty or absent.
func TestPluginInfoViewShowsAnEmptyGrantSet(t *testing.T) {
	got := pluginInfoView(plugins.PluginMetadata{Name: "p"}).String()
	if !strings.Contains(got, "grants") {
		t.Fatalf("a plugin with no grants dropped the grants row entirely:\n%s", got)
	}
}

// TestJoinOrNone covers the helper both permission views depend on. The
// "(none)" case is the one that matters: an empty join would render a
// grant change as "p grants:  -> " and read as broken output.
func TestJoinOrNone(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "(none)"},
		{[]string{}, "(none)"},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a, b"},
		{[]string{"a", "b", "c"}, "a, b, c"},
	}
	for _, tc := range cases {
		if got := joinOrNone(tc.in); got != tc.want {
			t.Errorf("joinOrNone(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPluginPermsView_ShowsBeforeAndAfter proves a permission change
// reports BOTH sides. A view that printed only the new set would leave an
// operator unable to tell what the change actually did.
func TestPluginPermsView_ShowsBeforeAndAfter(t *testing.T) {
	got := pluginPermsView{
		Name:   "cascade-claude",
		Before: []string{"read-context"},
		After:  []string{"read-context", "hook-emit"},
	}.String()
	if want := "cascade-claude grants: read-context -> read-context, hook-emit"; got != want {
		t.Fatalf("perms view = %q, want %q", got, want)
	}

	granted := pluginPermsView{Name: "p", Before: nil, After: []string{"net.http"}}.String()
	if want := "p grants: (none) -> net.http"; granted != want {
		t.Fatalf("first-grant view = %q, want %q", granted, want)
	}
	revoked := pluginPermsView{Name: "p", Before: []string{"net.http"}, After: nil}.String()
	if want := "p grants: net.http -> (none)"; revoked != want {
		t.Fatalf("last-revoke view = %q, want %q", revoked, want)
	}
}

// TestPluginUpdateView_CarriesTheRegistryNotice proves the notice is
// surfaced rather than dropped: it is how an operator learns the update
// ran without a reachable registry.
func TestPluginUpdateView_CarriesTheRegistryNotice(t *testing.T) {
	plain := pluginUpdateView{Name: "p", Message: "updated p to 2.0.0"}.String()
	if plain != "updated p to 2.0.0" {
		t.Fatalf("view without a notice = %q, want just the message", plain)
	}
	withNotice := pluginUpdateView{
		Name: "p", Message: "updated p to 2.0.0", RegistryNotice: "registry unreachable",
	}.String()
	if want := "updated p to 2.0.0 (registry unreachable)"; withNotice != want {
		t.Fatalf("view with a notice = %q, want %q", withNotice, want)
	}
}

// TestPluginAddView_IsItsMessage pins the simplest view, so a later change
// that starts decorating it has to say so here first.
func TestPluginAddView_IsItsMessage(t *testing.T) {
	if got := (pluginAddView{Message: "installed p"}).String(); got != "installed p" {
		t.Fatalf("add view = %q, want the bare message", got)
	}
}

// updateCmdWithFrom builds the minimal cobra command runPluginUpdate reads
// its --from flag off, so the refusal branches can be driven without a
// daemon, a store or a real manifest.
func updateCmdWithFrom(t *testing.T, from string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().String("from", "", "candidate manifest path")
	if from != "" {
		if err := cmd.Flags().Set("from", from); err != nil {
			t.Fatalf("set --from: %v", err)
		}
	}
	cmd.SetContext(context.Background())
	return cmd
}

// updateRefusalCase is one guard clause runPluginUpdate applies before it
// touches a store.
type updateRefusalCase struct {
	name    string
	args    []string
	from    string
	all     bool
	wantIn  string
	wantErr cascade.Kind
}

// updateRefusalCases is the table, lifted out of the test so the test body
// stays within the function-length gate.
func updateRefusalCases(t *testing.T) []updateRefusalCase {
	t.Helper()
	return []updateRefusalCase{
		{
			name:    "--all is refused with the per-plugin instruction",
			all:     true,
			wantIn:  "--from <path>",
			wantErr: cascade.KindUnsupported,
		},
		{
			name:    "a missing name is refused",
			wantIn:  "a plugin name is required",
			wantErr: cascade.KindInvalidInput,
		},
		{
			// X/S-50.T8: omitting --from no longer refuses outright — it
			// routes to the registry-driven path instead (plugin_update_
			// registry.go). This process's testPluginDeps has no
			// [registry] configured and no daemon configured, so that
			// path itself refuses, naming the actual gap rather than the
			// pre-T8 "--from is required" wording.
			name:    "a missing --from with no registry configured is refused",
			args:    []string{"some-plugin"},
			wantIn:  "[registry].url is not configured",
			wantErr: cascade.KindInvalidInput,
		},
		{
			name:    "an unreadable manifest is refused, naming the path",
			args:    []string{"some-plugin"},
			from:    filepath.Join(t.TempDir(), "absent.toml"),
			wantIn:  "absent.toml",
			wantErr: cascade.KindInvalidInput,
		},
	}
}

// TestRunPluginUpdate_Refusals pins every refusal the update verb makes
// before it touches a store. Each names what the operator must do next; a
// refusal that merely says "invalid" leaves them guessing.
func TestRunPluginUpdate_Refusals(t *testing.T) {
	deps := testPluginDeps(t)
	for _, tc := range updateRefusalCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			err := runPluginUpdate(updateCmdWithFrom(t, tc.from), deps, tc.args, "", tc.all, false, nil)
			if err == nil {
				t.Fatal("runPluginUpdate succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantIn)
			}
			if got, ok := cascade.KindOf(err); !ok || got != tc.wantErr {
				t.Errorf("error kind = %v (typed=%v), want %v", got, ok, tc.wantErr)
			}
		})
	}
}
