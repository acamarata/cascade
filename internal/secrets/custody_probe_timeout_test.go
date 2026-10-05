//go:build darwin

package secrets

// Purpose: a darwin availability probe that reaches its deadline refuses
//   with ErrProbeTimeout and SelectCustody never falls back to the file
//   vault for it (P1-SEC-42, BF-028 residual c): a slow keychain is not a
//   locked one. The deadline is decided from the probe's own ctx, so the
//   slow runner returns what exec returns for a killed process ("signal:
//   killed", no ctx.Err() inside it).
// Constraints: probeScript over fakeSecurity through Config.Runner only;
//   no wall-clock sleeps (a slow call blocks on ctx.Done); probe legs use a
//   50 ms parent deadline, and only the SelectCustody leg waits the real
//   probeTimeout. HOME and USERPROFILE are fresh temp dirs.

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// requireProbeTimeout asserts ErrProbeTimeout by identity, Kind and exact
// message (errors.Is on a cascade error compares Kind only).
func requireProbeTimeout(t *testing.T, err error) {
	t.Helper()
	var ce *cascade.Error
	if err != ErrProbeTimeout || !errors.As(err, &ce) {
		t.Fatalf("error = %v, want ErrProbeTimeout by identity", err)
	}
	const msg = "custody_probe_timeout: platform keychain probe hit its deadline; custody refused"
	if ce.Kind != cascade.KindUnavailable || ce.Msg != msg {
		t.Fatalf("error kind/message = %v / %q", ce.Kind, ce.Msg)
	}
}

// slowScript blocks the named subcommands until the probe's ctx is done.
func slowScript(subs ...string) *probeScript {
	p := newProbeScript()
	p.block = map[string]bool{}
	for _, sub := range subs {
		p.block[sub] = true
	}
	return p
}

// shortProbe runs one probe under a 50 ms parent deadline, shorter than
// probeTimeout, so the probe's own ctx inherits it.
func shortProbe(kc *keychainCustody) error {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	return kc.probe(ctx)
}

// unresolvedKeychain builds custody with no KeychainPath, so the probe
// resolves the default keychain through run; stat is stubbed to "exists".
func unresolvedKeychain(t *testing.T, run commandRunner) *keychainCustody {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	c, err := platformCustody(Config{Service: "cascade-probe-timeout-test", Runner: run})
	if err != nil {
		t.Fatalf("platformCustody: %v", err)
	}
	kc := c.(*keychainCustody)
	kc.stat = func(string) (os.FileInfo, error) { return nil, nil }
	return kc
}

func countSub(fake *fakeSecurity, sub string) int {
	n := 0
	for _, call := range fake.calls {
		if len(call) > 1 && call[1] == sub {
			n++
		}
	}
	return n
}

func TestProbeDeadlineRefusesWithoutFileVault(t *testing.T) {
	t.Run("probe legs", probeDeadlineLegs)
	t.Run("memo", probeDeadlineMemo)
	t.Run("cancel", probeDeadlineCancel)
	t.Run("select custody", probeDeadlineSelect)
}

func probeDeadlineLegs(t *testing.T) {
	legs := map[string]func(t *testing.T) *keychainCustody{
		"slow default-keychain": func(t *testing.T) *keychainCustody {
			return unresolvedKeychain(t, slowScript("default-keychain").run)
		},
		"slow add":    func(t *testing.T) *keychainCustody { return scriptedKeychain(t, slowScript("add-generic-password")) },
		"slow find":   func(t *testing.T) *keychainCustody { return scriptedKeychain(t, slowScript("find-generic-password")) },
		"slow delete": func(t *testing.T) *keychainCustody { return scriptedKeychain(t, slowScript("delete-generic-password")) },
		"slow add, ctx.Err() shape": func(t *testing.T) *keychainCustody {
			p := slowScript("add-generic-password")
			p.blockCtxErr = true
			return scriptedKeychain(t, p)
		},
		"set fails fast, cleanup slow": func(t *testing.T) *keychainCustody {
			p := slowScript("delete-generic-password")
			p.fake.fail["add-generic-password"] = lockedStderr
			return scriptedKeychain(t, p)
		},
	}
	for name, mk := range legs {
		t.Run(name, func(t *testing.T) { requireProbeTimeout(t, shortProbe(mk(t))) })
	}
}

func probeDeadlineMemo(t *testing.T) {
	kc := unresolvedKeychain(t, slowScript("default-keychain").run)
	for attempt := 1; attempt <= 2; attempt++ {
		requireProbeTimeout(t, shortProbe(kc))
	}
	fast := newProbeScript()
	kc.run = fast.run
	for attempt := 1; attempt <= 2; attempt++ {
		if err := kc.probe(context.Background()); err != nil {
			t.Fatalf("fast probe %d after two timeouts = %v; an expired resolution was cached", attempt, err)
		}
	}
	if n := countSub(fast.fake, "default-keychain"); n != 1 {
		t.Fatalf("default-keychain ran %d times across two successful probes, want 1 (a success is memoized)", n)
	}
}

// probeDeadlineCancel cancels the PARENT ctx while the first default-keychain
// lookup is in flight. That is not a deadline: the error is not
// ErrProbeTimeout, and the failure is never memoized (a cancelled request
// must not poison the instance as unavailable), so a fast runner on the same
// instance then probes clean.
func probeDeadlineCancel(t *testing.T) {
	slow := slowScript("default-keychain")
	inFlight, once := make(chan struct{}), sync.Once{}
	kc := unresolvedKeychain(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		once.Do(func() { close(inFlight) })
		return slow.run(ctx, name, args...)
	})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-inFlight
		cancel()
	}()
	err := kc.probe(parent)
	if err == nil || err == ErrProbeTimeout {
		t.Fatalf("probe cancelled mid-lookup = %v, want an error that is not ErrProbeTimeout", err)
	}
	if parent.Err() != context.Canceled {
		t.Fatalf("parent ctx = %v, want Canceled", parent.Err())
	}
	kc.mu.Lock()
	memoized := kc.resolved
	kc.mu.Unlock()
	if memoized {
		t.Fatal("a lookup failure seen under a cancelled ctx was memoized")
	}
	fast := newProbeScript()
	kc.run = fast.run
	if err := kc.probe(context.Background()); err != nil {
		t.Fatalf("fast probe after a cancelled one = %v; the cancelled failure was cached", err)
	}
}

func probeDeadlineSelect(t *testing.T) {
	scenarios := map[string]func() *probeScript{
		"slow add": func() *probeScript { return slowScript("add-generic-password") },
		"set fails fast, cleanup slow": func() *probeScript {
			p := slowScript("delete-generic-password")
			p.fake.fail["add-generic-password"] = lockedStderr
			return p
		},
	}
	for name, mk := range scenarios {
		t.Run(name, func(t *testing.T) {
			p, home := mk(), t.TempDir()
			start := time.Now()
			c, err := selectIn(t, home, p)
			elapsed := time.Since(start)
			if c != nil {
				t.Fatalf("selected %q although the probe hit its deadline", c.Name())
			}
			requireProbeTimeout(t, err)
			if found := onDisk(t, home); len(found) != 0 {
				t.Fatalf("a timed-out probe fell back to the file vault: %v", found)
			}
			if elapsed >= probeTimeout+3*time.Second || len(p.deadlines) == 0 {
				t.Fatalf("selection took %v over %d runner calls; want under %v through the injected runner",
					elapsed, len(p.deadlines), probeTimeout+3*time.Second)
			}
		})
	}
}
