package coretools_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
)

// TestMCPInboxToolsExactlyTwo pins the read-only inbox surface and compact membership.
func TestMCPInboxToolsExactlyTwo(t *testing.T) {
	want := map[string]string{
		"cascade_inbox_list": "inbox.list",
		"cascade_inbox_read": "inbox.read",
	}
	seen := map[string]bool{}
	for _, spec := range coretools.Specs() {
		if !strings.HasPrefix(spec.Name, "cascade_inbox_") {
			continue
		}
		method, ok := want[spec.Name]
		if !ok || seen[spec.Name] {
			t.Errorf("unexpected or duplicate inbox tool %q", spec.Name)
		}
		seen[spec.Name] = true
		if spec.Method != method || spec.Mutating || spec.Capability != "inbox.read" {
			t.Errorf("%s: method=%q mutating=%v capability=%q", spec.Name, spec.Method, spec.Mutating, spec.Capability)
		}
		if spec.Alias != "" {
			t.Errorf("%s exposes an unexpected alias %q", spec.Name, spec.Alias)
		}
		if !slices.Contains(mcp.CompactToolNames, spec.Name) {
			t.Errorf("%s is absent from CompactToolNames", spec.Name)
		}
		assertInboxSchema(t, spec)
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("missing inbox tool %s", name)
		}
	}
}

// assertInboxSchema checks the complete parameter set, including required fields.
func assertInboxSchema(t *testing.T, spec coretools.Spec) {
	t.Helper()
	if spec.Schema["type"] != "object" || spec.Schema["additionalProperties"] != false {
		t.Errorf("%s must have a closed object schema", spec.Name)
	}
	wantProps, wantRequired := map[string]string{"unread": "boolean"}, []string{}
	if spec.Name == "cascade_inbox_read" {
		wantProps, wantRequired = map[string]string{"id": "string"}, []string{"id"}
	}
	props, ok := spec.Schema["properties"].(map[string]any)
	if !ok || len(props) != len(wantProps) {
		t.Errorf("%s properties = %v, want %v", spec.Name, props, wantProps)
	}
	for name, raw := range props {
		leaf, ok := raw.(map[string]any)
		if !ok || wantProps[name] == "" || leaf["type"] != wantProps[name] {
			t.Errorf("%s unexpected property %s: %v", spec.Name, name, raw)
		}
	}
	if !reflect.DeepEqual(spec.Schema["required"], wantRequired) {
		t.Errorf("%s required = %v, want %v", spec.Name, spec.Schema["required"], wantRequired)
	}
}
