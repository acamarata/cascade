//go:build capmap

package capmap

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// probes is the probe registry: probe name to the function that proves one
// verified row. Probe files fill it from init through register.
var probes = map[string]func(*testing.T){}

// probeNamePattern is the only shape a probe name may take.
var probeNamePattern = regexp.MustCompile(`^TestCapmap_[A-Za-z0-9]+$`)

// addProbe adds fn to reg under name. It rejects a nil function, a name that
// does not match probeNamePattern and a name already registered.
func addProbe(reg map[string]func(*testing.T), name string, fn func(*testing.T)) error {
	switch {
	case fn == nil:
		return cascade.Newf(cascade.KindInvalidInput, "capmap: probe %q has a nil function", name)
	case !probeNamePattern.MatchString(name):
		return cascade.Newf(cascade.KindInvalidInput, "capmap: probe name %q does not match %s", name, probeNamePattern)
	}
	if _, dup := reg[name]; dup {
		return cascade.Newf(cascade.KindInvalidInput, "capmap: probe %q is already registered", name)
	}
	reg[name] = fn
	return nil
}

// register adds a probe to the package registry. It panics on a rejected
// registration, which fails the test binary at init, before any test runs.
func register(name string, fn func(*testing.T)) {
	if err := addProbe(probes, name, fn); err != nil {
		panic(err)
	}
}

// runProbe runs fn as a subtest named name and reports whether it passed.
// Passed means the body returned normally, the subtest did not fail and it
// did not skip. t.Run itself reports true for a skipped subtest and for one
// a -run filter excluded, and t.Skip ends the body through runtime.Goexit,
// so the deferred function records Failed and Skipped and a flag set after
// the body proves it ran to the end.
//
// Two probe behaviors are not counted as passed or contained: a probe that
// calls t.Parallel pauses until the parent returns, so the deferred function
// has not run and the probe is never recorded as passed (the subtest still
// prints PASS, but the table finding says it did not pass); and a probe that
// panics aborts the whole test binary, loudly, so no table check follows.
func runProbe(t *testing.T, name string, fn func(*testing.T)) bool {
	t.Helper()
	var returned, failed, skipped bool
	ok := t.Run(name, func(st *testing.T) {
		defer func() {
			failed = st.Failed()
			skipped = st.Skipped()
		}()
		fn(st)
		returned = true
	})
	return ok && returned && !failed && !skipped
}

// runProbes runs each named probe found in reg, in sorted order, and
// returns the set of names that passed. A name with no registered probe is
// left out of the set.
func runProbes(t *testing.T, names []string, reg map[string]func(*testing.T)) map[string]bool {
	t.Helper()
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	passed := map[string]bool{}
	for _, name := range sorted {
		if fn, found := reg[name]; found && runProbe(t, name, fn) {
			passed[name] = true
		}
	}
	return passed
}

// helperEnv selects the probe TestProbeHelperProcess runs. Unset, the test
// does nothing.
const helperEnv = "CAPMAP_PROBE_HELPER"

// helperProbes are the probe bodies that must not count as passed. They run
// in a child process because a failing subtest fails its parent test.
var helperProbes = map[string]func(*testing.T){
	"pass":    func(*testing.T) {},
	"errorf":  func(t *testing.T) { t.Errorf("probe failed") },
	"failnow": func(t *testing.T) { t.FailNow() },
}

// TestProbeHelperProcess is the child half of runProbeInChild. It is not a
// test on its own.
func TestProbeHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	fn, found := helperProbes[mode]
	if !found {
		t.Fatalf("unknown helper mode %q", mode)
	}
	_, _ = fmt.Fprintf(os.Stdout, "capmap-helper passed=%v\n", runProbe(t, "TestCapmap_Helper", fn))
}

// runProbeInChild runs helperProbes[mode] through runProbe in a child copy of
// this test binary, selected by the -test.run pattern run, and returns the
// verdict the child printed. It fails the test if the child printed no
// verdict, so a crash cannot read as "not passed".
func runProbeInChild(t *testing.T, mode, run string) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+run, "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	out, _ := cmd.Output() // a failing probe makes the child exit nonzero; the verdict line is what counts
	for _, line := range strings.Split(string(out), "\n") {
		switch strings.TrimSpace(line) {
		case "capmap-helper passed=true":
			return true
		case "capmap-helper passed=false":
			return false
		}
	}
	t.Fatalf("child for mode %q printed no verdict:\n%s", mode, out)
	return false
}

// skipFromGoroutine marks the test skipped from another goroutine, then
// returns normally. SkipNow ends only the calling goroutine, so the body
// reaches its end with Skipped() true: only the recorded Skipped flag, not
// the returned-to-the-end check, catches it. (The call goes through a method
// value because go vet rejects a direct t.SkipNow in a goroutine.)
func skipFromGoroutine(t *testing.T) {
	skip, done := t.SkipNow, make(chan struct{})
	go func() {
		defer close(done)
		skip()
	}()
	<-done
}

// skippingProbes skip in the ways a probe can.
var skippingProbes = map[string]func(*testing.T){
	"TestCapmap_Skip":              func(t *testing.T) { t.Skip("not available here") },
	"TestCapmap_Skipf":             func(t *testing.T) { t.Skipf("not available on %s", runtime.GOOS) },
	"TestCapmap_SkipNow":           func(t *testing.T) { t.SkipNow() },
	"TestCapmap_SkipFromGoroutine": skipFromGoroutine,
}

// childCases are the probes that run in a child process. The "filtered"
// case selects a subtest name that does not exist, so t.Run reports true
// without running the probe body.
var childCases = []struct {
	label, mode, run string
	want             bool
}{
	{"passing probe", "pass", "^TestProbeHelperProcess$", true},
	{"t.Errorf then return", "errorf", "^TestProbeHelperProcess$", false},
	{"t.FailNow", "failnow", "^TestProbeHelperProcess$", false},
	{"subtest excluded by -run", "pass", "^TestProbeHelperProcess$/^NoSuchProbe$", false},
}

// TestProbeSkipIsNotPass proves a probe that skips, fails or ends early is
// not in the passed set, and a probe that returns normally is.
func TestProbeSkipIsNotPass(t *testing.T) {
	for name, fn := range skippingProbes {
		t.Run("in-process/"+name, func(t *testing.T) {
			passed := runProbes(t, []string{name}, map[string]func(*testing.T){name: fn})
			if passed[name] {
				t.Fatalf("%s skipped but is in the passed set", name)
			}
		})
	}
	for _, c := range childCases {
		t.Run("child/"+c.label, func(t *testing.T) {
			if got := runProbeInChild(t, c.mode, c.run); got != c.want {
				t.Fatalf("probe mode %q with -run %q passed = %v, want %v", c.mode, c.run, got, c.want)
			}
		})
	}
	t.Run("in-process/passing probe is recorded", func(t *testing.T) {
		reg := map[string]func(*testing.T){"TestCapmap_Ok": func(*testing.T) {}}
		if passed := runProbes(t, []string{"TestCapmap_Ok", "TestCapmap_Unregistered"}, reg); !passed["TestCapmap_Ok"] || passed["TestCapmap_Unregistered"] {
			t.Fatalf("passed set = %v, want only TestCapmap_Ok", passed)
		}
	})
}

// TestProbeRegistry_RejectsBadRegistrations covers addProbe and register.
func TestProbeRegistry_RejectsBadRegistrations(t *testing.T) {
	noop := func(*testing.T) {}
	reg := map[string]func(*testing.T){}
	if err := addProbe(reg, "TestCapmap_Good1", noop); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	cases := map[string]struct {
		name string
		fn   func(*testing.T)
	}{
		"duplicate":    {"TestCapmap_Good1", noop},
		"wrong prefix": {"TestOther_Good", noop},
		"underscore":   {"TestCapmap_Bad_Name", noop},
		"empty suffix": {"TestCapmap_", noop},
		"nil func":     {"TestCapmap_Nil", nil},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			err := addProbe(reg, c.name, c.fn)
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("addProbe(%q) = %v, want an invalid-input error", c.name, err)
			}
		})
	}
	t.Run("register panics on a bad name", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("register did not panic on a bad name")
			}
		}()
		register("not-a-probe", noop)
	})
}
