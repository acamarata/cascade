//go:build darwin

package secrets

// Purpose: every darwin availability probe call writes, deletes and finds
//   its own nonce account (P1-SEC-42, BF-028 residual a), so two probes
//   against one healthy keychain never read each other's item.
// Constraints: one fakeSecurity behind a test mutex (the fake itself is not
//   goroutine-safe), reached only through Config.Runner with an explicit
//   fake keychain path; HOME and USERPROFILE are fresh temp dirs. Waits
//   select on ctx.Done, so a broken interleave fails at the probe bound
//   instead of hanging.

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// probeItems lists the probe items still stored in fake: the accounts that
// carry availabilityProbeAccount plus the nonce separator.
func probeItems(fake *fakeSecurity) []string {
	var out []string
	for account := range fake.items {
		if strings.HasPrefix(account, availabilityProbeAccount+".") {
			out = append(out, account)
		}
	}
	sort.Strings(out)
	return out
}

type probeTag struct{}

// probeCall is one recorded runner call: tag, subcommand, -a and -s.
type probeCall struct{ tag, sub, account, service string }

// lockedProbes serialises one fakeSecurity across goroutines, records every
// call, and holds a call back until a named earlier call has returned.
// Keys are tag + ":" + subcommand.
type lockedProbes struct {
	mu    sync.Mutex
	fake  *fakeSecurity
	log   []probeCall
	wait  map[string]chan struct{} // a call with this key waits for the channel
	after map[string]chan struct{} // the first call with this key closes it
}

func newLockedProbes() *lockedProbes {
	return &lockedProbes{fake: newFakeSecurity(), wait: map[string]chan struct{}{}, after: map[string]chan struct{}{}}
}

// runner tags each call with the parent context's probeTag, else fixed.
func (l *lockedProbes) runner(fixed string) commandRunner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		tag, ok := ctx.Value(probeTag{}).(string)
		if !ok {
			tag = fixed
		}
		key := tag + ":" + args[0]
		l.mu.Lock()
		gate := l.wait[key]
		l.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, &runnerError{err: ctx.Err()}
			}
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		l.log = append(l.log, probeCall{tag, args[0], flagValue(args, "-a"), flagValue(args, "-s")})
		out, err := l.fake.run(ctx, name, args...)
		if done := l.after[key]; done != nil {
			close(done)
			delete(l.after, key)
		}
		return out, err
	}
}

// interleave wires the race the fixed account lost: B's set waits for A's
// delete, and A's find waits for B's set. It returns A's set signal, which
// B's probe waits for before it starts.
func (l *lockedProbes) interleave() <-chan struct{} {
	aSet, aDel, bSet := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l.after["A:add-generic-password"] = aSet
	l.after["A:delete-generic-password"] = aDel
	l.after["B:add-generic-password"] = bSet
	l.wait["B:add-generic-password"] = aDel
	l.wait["A:find-generic-password"] = bSet
	return aSet
}

// probeAccount returns the one account tag used for its set, every delete
// and its find, failing when they differ or lack the nonce prefix.
func (l *lockedProbes) probeAccount(t *testing.T, tag, service string) string {
	t.Helper()
	seen, subs := map[string]bool{}, map[string]bool{}
	for _, c := range l.log {
		if c.tag != tag || c.sub == "default-keychain" {
			continue
		}
		if c.service != service {
			t.Fatalf("probe %s ran %s under service %q, want %q", tag, c.sub, c.service, service)
		}
		seen[c.account], subs[c.sub] = true, true
	}
	if len(seen) != 1 || !subs["add-generic-password"] || !subs["delete-generic-password"] || !subs["find-generic-password"] {
		t.Fatalf("probe %s used %d accounts over subcommands %v, want one account for set, delete and find", tag, len(seen), subs)
	}
	for account := range seen {
		if !strings.HasPrefix(account, availabilityProbeAccount+".") {
			t.Fatalf("probe %s account lacks the nonce prefix", tag)
		}
		return account
	}
	return ""
}

func lockedKeychain(t *testing.T, l *lockedProbes, tag string) *keychainCustody {
	t.Helper()
	c, err := platformCustody(Config{Service: "cascade-nonce-test", Runner: l.runner(tag), KeychainPath: fakeKeychainPath})
	if err != nil {
		t.Fatalf("platformCustody: %v", err)
	}
	return c.(*keychainCustody)
}

// raceProbes runs probeA and, once A's set landed, probeB; both must succeed.
func raceProbes(t *testing.T, aSet <-chan struct{}, probeA, probeB func() error) {
	t.Helper()
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = probeA() }()
	go func() { defer wg.Done(); <-aSet; errs[1] = probeB() }()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("probe A = %v, probe B = %v on a healthy keychain; want nil, nil", errs[0], errs[1])
	}
}

func requireDistinctNoLeak(t *testing.T, l *lockedProbes) {
	t.Helper()
	if a, b := l.probeAccount(t, "A", "cascade-nonce-test"), l.probeAccount(t, "B", "cascade-nonce-test"); a == b {
		t.Fatal("probes A and B shared one account")
	}
	if left := probeItems(l.fake); len(left) != 0 {
		t.Fatalf("%d probe items survived", len(left))
	}
}

func TestConcurrentProbesOnAHealthyKeychainBothSucceed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Run("interleave across two instances", interleaveTwoInstances)
	t.Run("free-running", freeRunningProbes)
	t.Run("interleave on one instance", interleaveOneInstance)
	t.Run("sequential on one instance", sequentialProbes)
}

func interleaveTwoInstances(t *testing.T) {
	l := newLockedProbes()
	kcA, kcB := lockedKeychain(t, l, "A"), lockedKeychain(t, l, "B")
	aSet := l.interleave()
	raceProbes(t, aSet, func() error { return kcA.probe(context.Background()) },
		func() error { return kcB.probe(context.Background()) })
	requireDistinctNoLeak(t, l)
}

func freeRunningProbes(t *testing.T) {
	l := newLockedProbes()
	kcs := []*keychainCustody{lockedKeychain(t, l, "A"), lockedKeychain(t, l, "B")}
	var wg sync.WaitGroup
	var failed sync.Map
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 4 {
				if !kcs[(g+i)%2].Available() {
					failed.Store(g*4+i, true)
				}
			}
		}()
	}
	wg.Wait()
	failed.Range(func(k, _ any) bool { t.Errorf("Available() call %v = false on a healthy keychain", k); return true })
	if left := probeItems(l.fake); len(left) != 0 {
		t.Fatalf("%d probe items survived", len(left))
	}
}

func interleaveOneInstance(t *testing.T) {
	l := newLockedProbes()
	kc := lockedKeychain(t, l, "")
	aSet := l.interleave()
	ctxA := context.WithValue(context.Background(), probeTag{}, "A")
	ctxB := context.WithValue(context.Background(), probeTag{}, "B")
	raceProbes(t, aSet, func() error { return kc.probe(ctxA) }, func() error { return kc.probe(ctxB) })
	requireDistinctNoLeak(t, l)
}

func sequentialProbes(t *testing.T) {
	l := newLockedProbes()
	kc := lockedKeychain(t, l, "S")
	for range 2 {
		if err := kc.probe(context.Background()); err != nil {
			t.Fatalf("probe: %v", err)
		}
	}
	var sets []string
	for _, c := range l.log {
		if c.sub == "add-generic-password" {
			sets = append(sets, c.account)
		}
	}
	suffix := regexp.MustCompile(`^[A-Z2-7]{26}$`)
	if len(sets) != 2 || sets[0] == sets[1] {
		t.Fatalf("two sequential probes wrote %d sets, distinct=%v; want two different accounts", len(sets), len(sets) == 2 && sets[0] != sets[1])
	}
	for _, account := range sets {
		if !suffix.MatchString(strings.TrimPrefix(account, availabilityProbeAccount+".")) {
			t.Fatal("a probe account suffix is not a crypto/rand.Text nonce")
		}
	}
}
