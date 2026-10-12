package doctor

// Purpose: table test of the widget check over every verdict row, the 2 s
//   probe bound, and the import boundary (internal/doctor must not import
//   internal/daemon or internal/client).
// SPORT: internal/doctor (P1-WID-04).

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeWidgetState struct {
	st  WidgetState
	err error
}

func (f fakeWidgetState) WidgetState(context.Context) (WidgetState, error) { return f.st, f.err }

type fakeWidgetProbe struct {
	err     error
	block   bool
	called  int
	timeout time.Duration
}

func (f *fakeWidgetProbe) ProbeStatusWidget(ctx context.Context) error {
	f.called++
	if dl, ok := ctx.Deadline(); ok {
		f.timeout = time.Until(dl)
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

func intPtr(i int) *int { return &i }

func healthy() WidgetState {
	return WidgetState{Installed: true, Managed: true, ProgramValid: true, Loaded: true,
		State: "running", BundlePath: "/Applications/CascadeMenuBar.app", BundlePresent: true,
		WindowLevel: intPtr(0), SelfReported: true, StatusPath: "/Users/example/widget-status.json"}
}

type widgetCase struct {
	name      string
	state     WidgetState
	stateErr  error
	probe     *fakeWidgetProbe
	want      Status
	contains  []string
	remedy    string
	wantProbe bool
}

func mutWidget(f func(*WidgetState)) WidgetState { s := healthy(); f(&s); return s }

func TestWidgetDoctorCheck(t *testing.T) {
	mut := mutWidget
	runWidgetCases(t, []widgetCase{
		{name: "not installed", state: WidgetState{}, probe: &fakeWidgetProbe{}, want: StatusOK, remedy: "cascade widget install"},
		{name: "bundle missing", state: mut(func(s *WidgetState) { s.BundlePresent = false }), probe: &fakeWidgetProbe{},
			want: StatusWarn, contains: []string{"/Applications/CascadeMenuBar.app"}},
		{name: "trash", state: mut(func(s *WidgetState) {
			s.Placement, s.ReportedBundlePath = WidgetPlacementTrash, "/Users/example/.Trash/CascadeMenuBar.app"
		}), probe: &fakeWidgetProbe{}, want: StatusWarn,
			contains: []string{"RUNNING_FROM_TRASH", "/Applications/CascadeMenuBar.app", "/Users/example/.Trash/CascadeMenuBar.app"}},
		{name: "moved", state: mut(func(s *WidgetState) {
			s.Placement, s.ReportedBundlePath = WidgetPlacementMoved, "/Volumes/X/CascadeMenuBar.app"
		}), probe: &fakeWidgetProbe{}, want: StatusWarn,
			contains: []string{"MOVED", "/Applications/CascadeMenuBar.app", "/Volumes/X/CascadeMenuBar.app"}},
		{name: "level -1 allowed", state: mut(func(s *WidgetState) { s.WindowLevel = intPtr(-1) }), probe: &fakeWidgetProbe{}, want: StatusOK, wantProbe: true},
		{name: "level -2 error", state: mut(func(s *WidgetState) { s.WindowLevel = intPtr(-2) }), probe: &fakeWidgetProbe{},
			want: StatusError, contains: []string{"-2"}},
		{name: "not loaded", state: mut(func(s *WidgetState) { s.Loaded = false }), probe: &fakeWidgetProbe{}, want: StatusWarn},
		{name: "no answer", state: healthy(), probe: &fakeWidgetProbe{err: errors.New("dial unix: no such file")},
			want: StatusError, contains: []string{"status.widget", "2s"}, wantProbe: true},
		{name: "unreadable state", state: WidgetState{}, stateErr: errors.New("boom"), probe: &fakeWidgetProbe{}, want: StatusError},
		{name: "healthy", state: healthy(), probe: &fakeWidgetProbe{}, want: StatusOK, wantProbe: true},
	})
}

// TestWidgetDoctorCheckTrustRows covers the identity and self-report rows.
func TestWidgetDoctorCheckTrustRows(t *testing.T) {
	mut := mutWidget
	runWidgetCases(t, []widgetCase{
		{name: "foreign plist", state: mut(func(s *WidgetState) { s.Managed, s.ProgramValid, s.BundlePresent = false, false, false }),
			probe: &fakeWidgetProbe{}, want: StatusError, remedy: "inspect ~/Library/LaunchAgents/com.acamarata.cascade.widget.plist and remove it if you did not create it",
			contains: []string{"a launch agent not written by cascade holds com.acamarata.cascade.widget"}},
		{name: "edited program", state: mut(func(s *WidgetState) { s.ProgramValid = false }),
			probe: &fakeWidgetProbe{}, want: StatusError, contains: []string{"does not run the CascadeMenuBar app"}},
		{name: "no self-report", state: mut(func(s *WidgetState) { s.SelfReported, s.WindowLevel, s.State = false, nil, "not running" }),
			probe: &fakeWidgetProbe{}, want: StatusWarn, contains: []string{"has not reported", "state not running", "/Users/example/widget-status.json"}},
		{name: "bad self-report", state: mut(func(s *WidgetState) {
			s.SelfReported, s.WindowLevel, s.SelfReportError = false, nil, "parse x: invalid character"
		}),
			probe: &fakeWidgetProbe{}, want: StatusWarn, contains: []string{"cannot be read", "invalid character"}},
		{name: "unsupported platform", state: WidgetState{Unsupported: true}, probe: &fakeWidgetProbe{}, want: StatusOK,
			contains: []string{"not supported"}},
	})
}

func runWidgetCases(t *testing.T, cases []widgetCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := NewWidgetCheck(fakeWidgetState{tc.state, tc.stateErr}, tc.probe).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertWidgetResult(t, res, tc.want, tc.contains, tc.remedy)
			if tc.name == "unsupported platform" && res.Remediation != "" {
				t.Errorf("unsupported platform offers remedy %q", res.Remediation)
			}
			if (tc.probe.called > 0) != tc.wantProbe {
				t.Errorf("probe called %d times, want called=%v", tc.probe.called, tc.wantProbe)
			}
		})
	}
}

// assertWidgetResult checks one widget verdict: its status, every expected
// message fragment, and the remediation when one is expected.
func assertWidgetResult(t *testing.T, res CheckResult, want Status, contains []string, remedy string) {
	t.Helper()
	if res.Status != want {
		t.Fatalf("status = %s (%q), want %s", res.Status, res.Message, want)
	}
	for _, s := range contains {
		if !strings.Contains(res.Message, s) {
			t.Errorf("message %q lacks %q", res.Message, s)
		}
	}
	if remedy != "" && res.Remediation != remedy {
		t.Errorf("remediation = %q, want %q", res.Remediation, remedy)
	}
}

func TestWidgetDoctorCheckProbeBound(t *testing.T) {
	p := &fakeWidgetProbe{}
	if _, err := NewWidgetCheck(fakeWidgetState{st: healthy()}, p).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.timeout <= time.Second || p.timeout > 2*time.Second {
		t.Fatalf("probe deadline = %s away, want just under 2s", p.timeout)
	}
	slow := &fakeWidgetProbe{block: true}
	c := widgetCheck{state: fakeWidgetState{st: healthy()}, probe: slow, timeout: 20 * time.Millisecond}
	res, _ := c.Run(context.Background())
	if res.Status != StatusError || !strings.Contains(res.Detail, "deadline") {
		t.Fatalf("a blocked probe gave %+v, want ERROR carrying the deadline error", res)
	}
}

func TestWidgetCheckMetadataAndMisconfiguration(t *testing.T) {
	c := NewWidgetCheck(nil, nil)
	if c.Name() != "widget" || c.Metadata().FirstRun || c.Describe() == "" {
		t.Fatalf("name/meta wrong: %q %+v", c.Name(), c.Metadata())
	}
	if _, err := c.Fix(context.Background()); !errors.Is(err, ErrCheckNotFixable) {
		t.Fatalf("Fix err = %v", err)
	}
	if res, _ := c.Run(context.Background()); res.Status != StatusError {
		t.Fatalf("nil reader status = %s, want error", res.Status)
	}
	if res, _ := NewWidgetCheck(fakeWidgetState{st: healthy()}, nil).Run(context.Background()); res.Status != StatusError {
		t.Fatalf("nil probe status = %s, want error", res.Status)
	}
}

func TestDoctorPackageDoesNotImportDaemonOrClient(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	found := false
	for _, f := range files {
		found = found || f == "widget_check.go"
	}
	if len(files) == 0 || !found {
		t.Fatalf("glob = %v, want non-empty and including widget_check.go", files)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // package source
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasSuffix(p, "/internal/daemon") || strings.Contains(p, "/internal/daemon/") || strings.HasSuffix(p, "/internal/client") {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}
