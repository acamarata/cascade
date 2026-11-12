// Purpose: pin the tool-gate pack's rendered command and default registration.
package hookpacks_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

func TestToolGatePackRenderedCommand(t *testing.T) {
	pack := hookpacks.ToolGatePack()
	if pack.Name != "tool-gate" || len(pack.Descriptors) != 1 || pack.Descriptors[0].EventType != hookpacks.EventPreToolUse {
		t.Fatalf("unexpected tool-gate pack: %+v", pack)
	}
	rendered, err := pack.RenderWithBinary("/tmp/s.sock", "/opt/cas cade/bin/cascade")
	if err != nil || len(rendered) != 1 {
		t.Fatalf("RenderWithBinary: %v, entries=%d", err, len(rendered))
	}
	var entry struct {
		Matcher string `json:"matcher"`
	}
	if err := json.Unmarshal(rendered[0], &entry); err != nil || entry.Matcher != "" {
		t.Fatalf("rendered matcher: %q, err=%v", entry.Matcher, err)
	}
	command := commandOf(t, pack, rendered, hookpacks.EventPreToolUse)
	if !strings.HasPrefix(command, "'/opt/cas cade/bin/cascade' fleet tool-gate ") ||
		!strings.Contains(command, "--socket '/tmp/s.sock'") || !strings.HasSuffix(command, "|| exit 2") {
		t.Fatalf("unexpected command: %q", command)
	}
	for _, forbidden := range []string{"curl", "sed", "|| true", "{{"} {
		if strings.Contains(command, forbidden) {
			t.Errorf("command contains %q: %q", forbidden, command)
		}
	}
	if _, err := pack.RenderWithBinary("/tmp/s.sock", "bin/cascade"); !errors.Is(err, hookpacks.ErrBinaryPathNotAbsolute) {
		t.Errorf("relative binary: got %v, want ErrBinaryPathNotAbsolute", err)
	}
	found := make(map[string]bool)
	for _, registered := range hookpacks.DefaultRegistry.Packs() {
		found[registered.Name] = true
	}
	for _, name := range []string{"tool-gate", "sessions"} {
		if !found[name] {
			t.Errorf("DefaultRegistry is missing %q", name)
		}
	}
}
