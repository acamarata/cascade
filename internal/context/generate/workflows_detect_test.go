package generate

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWorkflowsProbeFromLookPath(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in executable is a POSIX file mode")
	}
	p := NewExecProbe()
	if p.LookPath == nil || p.Run == nil {
		t.Fatal("NewExecProbe left LookPath or Run unset")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var calls []runCall
	p.Run = replayProbe("2.1.316 (claude CLI)\n", 0, "Commands:\n  agents  x\n", 0, &calls).Run
	r, err := p.Probe(t.Context())
	if err != nil || r.Binary != bin || r.Result != resultUnsupported {
		t.Fatalf("probe = %+v %v, want binary %s", r, err, bin)
	}
	if len(calls) != 2 || calls[0].bin != bin {
		t.Fatalf("calls = %+v", calls)
	}
	t.Setenv("PATH", t.TempDir())
	calls = nil
	r, err = p.Probe(t.Context())
	if err != nil || r.Result != resultUnsupported || r.Binary != "" || len(calls) != 0 {
		t.Fatalf("absent binary: %+v %v, %d runs", r, err, len(calls))
	}
	if deferralReason(r) != "harness CLI not found on PATH; no workflows rendered" {
		t.Fatalf("absent reason %q", deferralReason(r))
	}
}

func TestWorkflowsProbeArgvOnlyHelpAndVersion(t *testing.T) {
	home := isolate(t)
	t.Setenv("CASCADE_TOKEN", "never-forwarded")
	var calls []runCall
	var homes []string
	for _, name := range []string{"supported-fixture.json", "unsupported-fixture.json", "out-of-range-fixture.json"} {
		p := fixtureProbe(t, name, &calls)
		inner := p.Run
		p.Run = func(ctx context.Context, bin string, args, env []string) ([]byte, int, error) {
			for _, kv := range env {
				if h, ok := strings.CutPrefix(kv, "HOME="); ok {
					ents, err := os.ReadDir(h)
					if err != nil || len(ents) != 0 {
						t.Errorf("probe HOME %s is not an existing empty dir (%v, %d entries)", h, err, len(ents))
					}
					homes = append(homes, h)
				}
			}
			return inner(ctx, bin, args, env)
		}
		if _, err := p.Probe(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 6 {
		t.Fatalf("%d invocations, want 6", len(calls))
	}
	for i, c := range calls {
		want := []string{"--version", "--help"}[i%2]
		if strings.Join(c.args, " ") != want {
			t.Errorf("call %d argv %v, want [%s]", i, c.args, want)
		}
		if len(c.env) != 2 || c.env[0] != "PATH="+os.Getenv("PATH") || !strings.HasPrefix(c.env[1], "HOME=") {
			t.Errorf("call %d env %v, want exactly PATH and HOME", i, c.env)
		}
		if strings.Contains(strings.Join(c.env, "\n"), "CASCADE_") {
			t.Errorf("call %d forwarded a CASCADE_ variable", i)
		}
	}
	for _, h := range homes {
		if h == home || h == os.Getenv("HOME") {
			t.Fatalf("probe used the caller's HOME %s", h)
		}
		if _, err := os.Stat(h); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("probe HOME %s left behind (%v)", h, err)
		}
	}
}

func TestWorkflowsRealCCProbe(t *testing.T) {
	isolate(t)
	want := map[string]struct {
		result    string
		synthetic bool
	}{
		"supported-fixture.json":    {resultSupported, true},
		"unsupported-fixture.json":  {resultUnsupported, false},
		"out-of-range-fixture.json": {resultOutOfRange, true},
	}
	for name, w := range want {
		f := loadCCFixture(t, name)
		if f.Tool == "" || f.Version == "" || f.CapturedAt == "" || f.Command == "" || f.Stdout == "" {
			t.Fatalf("%s: empty provenance field %+v", name, f)
		}
		if f.Tool != "claude" || f.Command != "claude --help" || f.ExitCode != 0 || f.Synthetic != w.synthetic {
			t.Fatalf("%s: tool %q command %q exit %d synthetic %v", name, f.Tool, f.Command, f.ExitCode, f.Synthetic)
		}
		var calls []runCall
		r, err := fixtureProbe(t, name, &calls).Probe(t.Context())
		if err != nil || r.Result != w.result || r.Version != f.Version {
			t.Fatalf("%s: replay = %+v %v, want %s at %s", name, r, err, w.result, f.Version)
		}
	}
}

func TestRealCCWorkflowCapturesScrubbed(t *testing.T) {
	for _, probe := range []string{"/Users/x", "/home/x", "a.b@example.org"} {
		if !scrubLeak.MatchString(probe) {
			t.Fatalf("scrub pattern misses %q", probe)
		}
	}
	var n int
	err := filepath.WalkDir(filepath.Join("testdata", "real-cc-workflows"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if loc := scrubLeak.FindIndex(data); loc != nil {
			t.Errorf("%s leaks a home path or an email at byte %d", p, loc[0])
		}
		return nil
	})
	if err != nil || n != 4 {
		t.Fatalf("walked %d captures (%v), want README and three fixtures", n, err)
	}
}

func TestWorkflowsProbeFailures(t *testing.T) {
	isolate(t)
	for _, p := range []*ExecProbe{nil, {}, {LookPath: func(string) (string, error) { return probeBin, nil }}} {
		_, err := p.Probe(t.Context())
		wantKind(t, err, cascade.KindInvalidInput, "generate: workflows probe needs LookPath and Run")
	}
	broken := &ExecProbe{LookPath: func(string) (string, error) { return probeBin, nil },
		Run: func(context.Context, string, []string, []string) ([]byte, int, error) {
			return nil, 0, errors.New("exec format error")
		}}
	if r, err := broken.Probe(t.Context()); err != nil || r.Result != resultIndeterminate || r.ExitCode != -1 {
		t.Fatalf("unrunnable binary = %+v %v, want indeterminate", r, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var calls []runCall
	_, err := fixtureProbe(t, "supported-fixture.json", &calls).Probe(ctx)
	if ce, ok := err.(*cascade.Error); !ok || ce.Kind != cascade.KindCanceled {
		t.Fatalf("canceled probe error = %v", err)
	}
	slow := &ExecProbe{LookPath: broken.LookPath, Run: func(ctx context.Context, _ string, _, _ []string) ([]byte, int, error) {
		if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > probeTimeout {
			t.Errorf("invocation deadline %v %v, want at most %s", dl, ok, probeTimeout)
		}
		return []byte("2.1.300\n"), 0, nil
	}}
	if r, _ := slow.Probe(t.Context()); r.Result != resultIndeterminate {
		t.Fatalf("help without Commands: = %+v", r)
	}
}

func TestWorkflowsRunIsolated(t *testing.T) {
	isolate(t)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go binary on PATH")
	}
	home := t.TempDir()
	settleDir(t, filepath.Dir(home))
	// No build cache and no toolchain download: the probe must not write
	// anywhere under the fake home while it is being removed.
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GOCACHE=off", "GOTOOLCHAIN=local"}
	out, exit, err := runIsolated(t.Context(), goBin, []string{"version"}, env)
	if err != nil || exit != 0 || !bytes.HasPrefix(out, []byte("go version ")) {
		t.Fatalf("go version = %q %d %v", out, exit, err)
	}
	if _, exit, err = runIsolated(t.Context(), goBin, []string{"no-such-subcommand"}, env); err != nil || exit == 0 {
		t.Fatalf("failing command = exit %d %v, want a non-zero exit and no error", exit, err)
	}
	if _, exit, err = runIsolated(t.Context(), filepath.Join(t.TempDir(), "absent"), nil, env); err == nil || exit != -1 {
		t.Fatalf("absent program = exit %d %v, want -1 and an error", exit, err)
	}
	var b bytes.Buffer
	w := &capWriter{w: &b, left: 3}
	if n, err := w.Write([]byte("abcdef")); n != 6 || err != nil || b.String() != "abc" {
		t.Fatalf("capWriter kept %q (%d %v)", b.String(), n, err)
	}
}

// settleDir removes dir at cleanup, retrying while a straggling child of the
// probe is still writing into it, so t.TempDir's own removal finds it gone.
func settleDir(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		for range 20 {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}
