package main

// Purpose: both renderings of `provider add` (P1-E16-W4-S35-T10) — the
//   human block and the JSON envelope — and that they agree.
// Constraints: asserted on json.Marshal(view) and view.String() directly,
//   because the defect was a result handed to the writer with no view at
//   all: the human path got Go's default struct formatting and the JSON
//   path got Go's field names. A test that drove the command and only
//   checked the exit code passed through both.
// SPORT: cmd/cascade provider add view (ADD) — P1-E16-W4-S35-T10.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/pkg/provider"
)

// addResultFixture is one completed intake, with a probed capability set
// so the summary has something to say.
func addResultFixture() intake.AddResult {
	return intake.AddResult{
		Status:   "converged",
		Warnings: []string{"the live micro-verify was skipped"},
		Record: intake.ProviderRecord{
			Name:        "acme",
			Driver:      intake.DriverOpenAICompat,
			BaseURL:     "https://api.example.invalid",
			Auth:        intake.AuthKey,
			AuthRef:     "provider.acme.key",
			KnownModels: []string{"acme-large", "acme-small"},
			Capabilities: provider.Capabilities{
				ToolUse: provider.CapabilitySupported,
				Vision:  provider.CapabilityUnsupported,
			},
		},
	}
}

// TestProviderAddRendersASentenceNotAStruct is the regression. The exact
// strings matter less than the shape: a Go struct dump has no field names
// in it, so a rendering that omits the labels is the defect returning.
func TestProviderAddRendersASentenceNotAStruct(t *testing.T) {
	got := (providerAddView{AddResult: addResultFixture()}).String()

	for _, want := range []string{
		"provider", "acme",
		"status", "converged",
		"driver", "openai-compat",
		"endpoint", "https://api.example.invalid",
		"credential", "provider.acme.key",
		"models", "acme-large",
		"capabilities", "tool-use",
		"warning: the live micro-verify was skipped",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering omits %q:\n%s", want, got)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Errorf("the rendering is a struct dump:\n%s", got)
	}
}

// TestProviderAddJSONIsSnakeCaseThroughout: --json is an external
// contract, and a consumer keying on the documented spelling has to find
// the value. The capabilities sub-object was the one that leaked Go's own
// names, so it is checked by name rather than by a whole-document
// comparison that would pass if the key moved.
func TestProviderAddJSONIsSnakeCaseThroughout(t *testing.T) {
	raw, err := json.Marshal(providerAddView{AddResult: addResultFixture()})
	if err != nil {
		t.Fatalf("marshalling the view: %v", err)
	}
	doc := string(raw)

	for _, want := range []string{
		`"provider_name":"acme"`, `"driver_kind":"openai-compat"`,
		`"tool_use":"supported"`, `"vision":"unsupported"`, `"search":"unknown"`,
		`"compliance_posture"`, `"auth_modes"`, `"credential_sharing"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the envelope omits %s:\n%s", want, doc)
		}
	}
	for _, unwanted := range []string{`"Search"`, `"ToolUse"`, `"AuthModes"`, `"Providers"`} {
		if strings.Contains(doc, unwanted) {
			t.Errorf("the envelope carries Go's own field name %s:\n%s", unwanted, doc)
		}
	}
	// The ordinal is what a tri-state must never be on the wire: 0, 1 and
	// 2 say nothing on their own and change meaning if a member is
	// inserted.
	if strings.Contains(doc, `"tool_use":1`) {
		t.Errorf("a capability is encoded as its ordinal:\n%s", doc)
	}
}

// TestBothSurfacesAgreeAboutACapability is the assertion that would have
// caught the disagreement: the human line said "unknown" for six
// dimensions while --json said 0 for the same six.
func TestBothSurfacesAgreeAboutACapability(t *testing.T) {
	view := providerAddView{AddResult: addResultFixture()}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshalling the view: %v", err)
	}
	human := view.String()

	if !strings.Contains(human, "tool-use") {
		t.Errorf("the human rendering does not name the supported capability:\n%s", human)
	}
	if !strings.Contains(string(raw), `"tool_use":"supported"`) {
		t.Errorf("the envelope does not name the supported capability:\n%s", raw)
	}
}

// TestAnUnprobedLaneSaysSoRatherThanListingSixUnknowns: the tri-state's
// point is that unknown and unsupported differ, and six "unknown"s tell an
// operator less than one sentence saying the probe has not run.
func TestAnUnprobedLaneSaysSoRatherThanListingSixUnknowns(t *testing.T) {
	result := addResultFixture()
	result.Record.Capabilities = provider.Capabilities{}
	if got := (providerAddView{AddResult: result}).String(); !strings.Contains(got, "not probed") {
		t.Errorf("an unprobed lane does not say so:\n%s", got)
	}

	result.Record.Capabilities = provider.Capabilities{Vision: provider.CapabilityUnsupported}
	got := (providerAddView{AddResult: result}).String()
	if strings.Contains(got, "not probed") {
		t.Errorf("a probed lane with nothing supported reads as unprobed:\n%s", got)
	}
	if !strings.Contains(got, "none of the probed") {
		t.Errorf("a probed lane with nothing supported says nothing useful:\n%s", got)
	}
}

// TestAnEmptyModelListIsStated: a blank column reads as a rendering bug,
// and "the endpoint enumerated no models" is a real state -- it is the one
// the micro-verify treats as a verify failure.
func TestAnEmptyModelListIsStated(t *testing.T) {
	result := addResultFixture()
	result.Record.KnownModels = nil
	if got := (providerAddView{AddResult: result}).String(); !strings.Contains(got, "none enumerated") {
		t.Errorf("an empty model list is rendered as a blank:\n%s", got)
	}
}

// TestProviderListJSONIsSnakeCaseToo: `provider list` carried "Providers",
// capitalised, because the field had no tag. It is the same defect one
// subcommand over, and the acceptance suite in internal/runtime/init
// decodes the corrected spelling.
func TestProviderListJSONIsSnakeCaseToo(t *testing.T) {
	raw, err := json.Marshal(providerListResult{Providers: []providerListRow{{Name: "acme"}}})
	if err != nil {
		t.Fatalf("marshalling the list result: %v", err)
	}
	doc := string(raw)
	if !strings.Contains(doc, `"providers"`) {
		t.Errorf("the list envelope does not carry a snake_case providers key:\n%s", doc)
	}
	if strings.Contains(doc, `"Providers"`) {
		t.Errorf("the list envelope carries Go's own field name:\n%s", doc)
	}
}
