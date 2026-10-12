// Purpose: platform-neutral tests for the widget read side: the captured
//
//	`launchctl print` output (testdata/launchctl_print_widget.txt), the plist
//	reader and the RUNNING_FROM_TRASH / MOVED classification.
//
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const widgetTestTarget = "gui/501/" + WidgetLabel

func TestParseLaunchctlPrintCapturedOutput(t *testing.T) {
	out, err := os.ReadFile(filepath.Join("testdata", "launchctl_print_widget.txt"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, state := parseLaunchctlPrint(out, widgetTestTarget)
	if !loaded || state != "not running" {
		t.Fatalf("captured output parsed as loaded=%v state=%q, want loaded=true state=%q", loaded, state, "not running")
	}
	if loaded, _ := parseLaunchctlPrint(out, "gui/501/com.example.other"); loaded {
		t.Fatal("output for another label parsed as loaded")
	}
}

func TestParseLaunchctlPrintNotLoaded(t *testing.T) {
	for name, out := range map[string]string{
		"empty":    "",
		"error":    "Bad request.\nCould not find service \"" + WidgetLabel + "\" in domain for user gui: 501\n",
		"no-state": widgetTestTarget + " = {\n\tactive count = 0\n}\n",
	} {
		loaded, state := parseLaunchctlPrint([]byte(out), widgetTestTarget)
		if name == "no-state" {
			if !loaded || state != "" {
				t.Errorf("%s: loaded=%v state=%q, want loaded with empty state", name, loaded, state)
			}
			continue
		}
		if loaded || state != "" {
			t.Errorf("%s: loaded=%v state=%q, want not loaded", name, loaded, state)
		}
	}
}

func TestWidgetPlistRoundTrip(t *testing.T) {
	env := map[string]string{"CASCADE_CLI_PATH": "/a & b/cascade", "CASCADE_HOME": "/h"}
	data := renderWidgetPlist("/Applications/CascadeMenuBar.app/Contents/MacOS/CascadeMenuBar", env, "/logs/widget.log")
	p, err := parseWidgetPlist(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Environment, env) {
		t.Fatalf("environment = %v, want %v (the Label string must not leak in)", p.Environment, env)
	}
	if !isManagedWidgetPlist(data) || isManagedWidgetPlist([]byte("<plist/>")) {
		t.Fatal("managed marker detection wrong")
	}
	if _, err := parseWidgetPlist([]byte("<plist><dict>")); err == nil {
		t.Fatal("malformed plist parsed without error")
	}
}

func TestClassifyPlacement(t *testing.T) {
	installed := "/Users/example/Applications/CascadeMenuBar.app"
	cases := []struct {
		name, reported, want string
	}{
		{"same", installed, PlacementOK},
		{"trash", "/Users/example/.Trash/CascadeMenuBar.app", PlacementRunningFromTrash},
		{"moved", "/Volumes/Other/CascadeMenuBar.app", PlacementMoved},
		{"other-volume-trash", "/Volumes/X/.Trashes/501/CascadeMenuBar.app", PlacementRunningFromTrash},
		{"unreported", "", PlacementOK},
	}
	for _, tc := range cases {
		got := classifyPlacement(installed, &WidgetSelfReport{BundlePath: tc.reported})
		if got != tc.want {
			t.Errorf("%s: placement = %q, want %q", tc.name, got, tc.want)
		}
	}
	if classifyPlacement(installed, nil) != PlacementOK {
		t.Error("nil self-report must classify as OK")
	}
	if r, err := parseSelfReport([]byte("{not json")); err == nil || r != nil {
		t.Errorf("malformed self-report = %+v, %v; want a parse error", r, err)
	}
}
