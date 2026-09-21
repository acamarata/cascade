package review

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestManifestIsValid proves this plugin's manifest passes the host's own
// validator, so BuiltinRegistry.Load indexes it rather than rejecting it.
func TestManifestIsValid(t *testing.T) {
	if errs := plugin.Validate(manifest()); len(errs) > 0 {
		t.Fatalf("manifest rejected by the host validator: %v", errs)
	}
}

// TestPluginIsRegisteredWithTheHost proves the init() registration reaches
// plugin.Builtins() -- reachable, matching the ticket's opt-in acceptance
// criterion ("registered in the C/S-05.T7 builtin registry").
func TestPluginIsRegisteredWithTheHost(t *testing.T) {
	for _, reg := range plugin.Builtins() {
		if reg.Manifest.ID == pluginID {
			if reg.Handlers == nil {
				t.Fatal("registered with nil handlers")
			}
			return
		}
	}
	t.Fatalf("%s is not in plugin.Builtins(); the init() registration never ran", pluginID)
}

// TestOptInDisabledByDefault_NoCommands proves the ticket's own opt-in
// acceptance criterion: this plugin declares no CommandSpec yet (T5 adds
// one), so a fresh binary mounts no `review` noun. Symbol-split assertion:
// every engine symbol lives under internal/review, never here -- proved by
// this file's own package boundary (plugins/review imports pkg/plugin and
// pkg/provider only; internal/review is unreachable from it, enforced by
// the depguard plugins-providers-boundary rule at compile time).
func TestOptInDisabledByDefault_NoCommands(t *testing.T) {
	m := manifest()
	if len(m.Provides.Commands) != 0 {
		t.Fatalf("manifest declares %d commands, want 0: P1-E25-W5-S52-T5 owns adding the \"review\" CommandSpec, not this ticket", len(m.Provides.Commands))
	}
}

// TestManifestMatchesManifestTOML proves the authored TOML and the in-code
// manifest have not drifted apart.
func TestManifestMatchesManifestTOML(t *testing.T) {
	raw, err := os.ReadFile("manifest.toml")
	if err != nil {
		t.Fatalf("read manifest.toml: %v", err)
	}
	toml := string(raw)
	m := manifest()
	for _, want := range []string{
		`id = "` + m.ID + `"`,
		`name = "` + m.Name + `"`,
		`schema = "` + m.Schema + `"`,
		`version = "` + m.Version + `"`,
		`host_version = "` + m.HostVersion + `"`,
		`runtime = "` + string(m.Runtime) + `"`,
	} {
		if !strings.Contains(toml, want) {
			t.Errorf("manifest.toml is missing %s", want)
		}
	}
}

// TestDispatchToolAndIntentAreRealRefusals proves DispatchTool/
// DispatchIntent are genuine "declares none" refusals, not stubs standing
// in for an unimplemented capability.
func TestDispatchToolAndIntentAreRealRefusals(t *testing.T) {
	h := handlers{}
	if _, err := h.DispatchTool(context.Background(), "anything", nil); err == nil {
		t.Error("DispatchTool returned nil error for an undeclared tool")
	}
	if _, err := h.DispatchIntent(context.Background(), "anything", nil); err == nil {
		t.Error("DispatchIntent returned nil error for an undeclared intent")
	}
}

// TestRunCommandRejectsUnknownName proves RunCommand refuses every name --
// this plugin declares no commands yet (T5's job).
func TestRunCommandRejectsUnknownName(t *testing.T) {
	if err := (handlers{}).RunCommand(context.Background(), "review", []string{"--diff", "-"}); err == nil {
		t.Error("RunCommand(\"review\", ...) returned nil error: this plugin declares no commands yet")
	}
}

// TestSetReviewProviderRefusesNil proves the injected seam fails closed on
// a nil provider, mirroring plugins/claude's Generate/SetGenerator pattern.
func TestSetReviewProviderRefusesNil(t *testing.T) {
	if err := SetReviewProvider(nil); err == nil {
		t.Error("SetReviewProvider(nil) returned nil error")
	}
}

// TestUnwiredReviewProviderIsHonest proves the default seam reports,
// truthfully, that nothing has wired it -- never a fabricated result.
func TestUnwiredReviewProviderIsHonest(t *testing.T) {
	u := unwiredReviewProvider{}
	if _, err := u.Review(context.Background(), provider.ReviewRequest{}); err == nil {
		t.Error("unwiredReviewProvider.Review returned nil error")
	}
	if _, err := u.Capabilities(context.Background(), ""); err == nil {
		t.Error("unwiredReviewProvider.Capabilities returned nil error")
	}
}

// TestSetReviewProviderInstalls proves a real SetReviewProvider call
// actually replaces the package-level seam RunCommand/reviewProvider read
// from -- the injection point review_wiring.go's init() uses in
// production.
func TestSetReviewProviderInstalls(t *testing.T) {
	t.Cleanup(func() { reviewProvider = unwiredReviewProvider{} })
	fake := fakeReviewProvider{}
	if err := SetReviewProvider(fake); err != nil {
		t.Fatalf("SetReviewProvider: %v", err)
	}
	if reviewProvider != provider.ReviewProvider(fake) {
		t.Error("SetReviewProvider did not install the given provider")
	}
}

// fakeReviewProvider is a minimal test-only provider.ReviewProvider double.
type fakeReviewProvider struct{}

func (fakeReviewProvider) Review(context.Context, provider.ReviewRequest) (provider.ReviewResponse, error) {
	return provider.ReviewResponse{}, nil
}
func (fakeReviewProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, nil
}
