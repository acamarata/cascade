//go:build darwin

package secrets

// Purpose: the darwin availability probe's CLEANUP contract (CX2-002).
//   The probe writes a real item; a probe whose delete fails has left that
//   item behind on a keychain that accepted the write, and must report
//   ErrProbeCleanupFailed rather than "available" or a plain unavailable.
// Constraints: every call goes through probeScript over fakeSecurity (the
//   Config.Runner seam) with an explicit fake keychain path, so no test can
//   reach /usr/bin/security or the real login keychain.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	permissionStderr = "security: SecKeychainItemDelete: Write permissions error."
	notFoundStderr   = "security: SecKeychainItemDelete: The specified item could not be found in the keychain."
	// spoofStderr carries the not-found phrase beside a denial. Text
	// matching alone would call it absence; the item is still there.
	spoofStderr  = "security: User interaction is not allowed. The specified item could not be found in the keychain."
	lockedStderr = "security: SecKeychainItemCreateFromContent: User interaction is not allowed."
	// findDeniedStderr is a permission-style failure of the post-delete
	// find: an error that is not a not-found, so absence is unproven.
	findDeniedStderr = "security: SecKeychainSearchCopyNext: User interaction is not allowed."
	// transientStderr looks retryable (a timeout), which is not absence.
	transientStderr = "security: SecKeychainItemDelete: The operation couldn't be completed. Operation timed out."
)

// probeScript wraps fakeSecurity. deleteStderr[i] fails the i-th delete
// with that stderr ("" lets the fake answer); a notFoundStderr entry also
// removes the item first, because a real not-found means it is gone.
// keepItem makes a delete report success while the item stays. findStderr,
// when set, makes every find fail with that stderr, whatever is stored.
type probeScript struct {
	fake         *fakeSecurity
	deleteStderr []string
	keepItem     bool
	findStderr   string
	sets, dels   int
	idents       [][2]string // (-a, -s) of every set and delete
	deadlines    []time.Time
	noDeadline   int
}

func newProbeScript(deleteStderr ...string) *probeScript {
	return &probeScript{fake: newFakeSecurity(), deleteStderr: deleteStderr}
}

func (p *probeScript) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if dl, ok := ctx.Deadline(); ok {
		p.deadlines = append(p.deadlines, dl)
	} else {
		p.noDeadline++
	}
	if err := ctx.Err(); err != nil {
		return nil, &runnerError{err: err}
	}
	switch args[0] {
	case "add-generic-password":
		p.sets++
		p.idents = append(p.idents, [2]string{flagValue(args, "-a"), flagValue(args, "-s")})
	case "find-generic-password":
		if p.findStderr != "" {
			return nil, &runnerError{err: errors.New("exit status 36"), stderr: p.findStderr}
		}
	case "delete-generic-password":
		p.dels++
		p.idents = append(p.idents, [2]string{flagValue(args, "-a"), flagValue(args, "-s")})
		if p.dels <= len(p.deleteStderr) && p.deleteStderr[p.dels-1] != "" {
			stderr := p.deleteStderr[p.dels-1]
			if stderr == notFoundStderr {
				delete(p.fake.items, flagValue(args, "-a"))
			}
			return nil, &runnerError{err: errors.New("exit status 1"), stderr: stderr}
		}
		if p.keepItem {
			return nil, nil
		}
	}
	return p.fake.run(ctx, name, args...)
}

func scriptedKeychain(t *testing.T, p *probeScript) *keychainCustody {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	c, err := platformCustody(Config{Service: "cascade-probe-cleanup-test", Runner: p.run, KeychainPath: fakeKeychainPath})
	if err != nil {
		t.Fatalf("platformCustody: %v", err)
	}
	return c.(*keychainCustody)
}

// requireCleanupRefusal asserts the exact sentinel by identity, kind and
// message (errors.Is on a cascade error compares Kind only, so it would
// accept any unavailable) and that the probe item is really still stored.
// It returns the deletes the first probe ran; the script must script
// failures for two probes, since Available is asserted too.
func requireCleanupRefusal(t *testing.T, kc *keychainCustody, p *probeScript) int {
	t.Helper()
	err := kc.probe(context.Background())
	dels := p.dels
	requireProbeCleanupFailed(t, err)
	if _, present := p.fake.items[availabilityProbeAccount]; !present {
		t.Fatal("scenario broken: the probe item is gone, so a refusal proves nothing")
	}
	if kc.Available() {
		t.Fatal("Available() = true although the probe item could not be removed")
	}
	return dels
}

func requireProbeCleanupFailed(t *testing.T, err error) {
	t.Helper()
	var ce *cascade.Error
	if err != ErrProbeCleanupFailed || !errors.As(err, &ce) {
		t.Fatalf("error = %v, want ErrProbeCleanupFailed by identity", err)
	}
	if ce.Kind != cascade.KindUnavailable || ce.Msg != "platform keychain probe could not clean up; custody refused" {
		t.Fatalf("error kind/message = %v / %q", ce.Kind, ce.Msg)
	}
}

func TestAvailableRefusesWhenProbeCleanupFails(t *testing.T) {
	p := newProbeScript(permissionStderr, permissionStderr, permissionStderr, permissionStderr)
	requireCleanupRefusal(t, scriptedKeychain(t, p), p)
}

func TestAvailableCleanupRetryBounded(t *testing.T) {
	p := newProbeScript(permissionStderr, permissionStderr, permissionStderr, permissionStderr)
	kc := scriptedKeychain(t, p)
	if kc.Available() {
		t.Fatal("Available() = true with every delete failing")
	}
	if p.sets != 1 || p.dels != 2 {
		t.Fatalf("runner saw %d sets and %d deletes, want exactly 1 and 2", p.sets, p.dels)
	}
	for _, id := range p.idents {
		if id != [2]string{availabilityProbeAccount, "cascade-probe-cleanup-test"} {
			t.Fatalf("a probe call used identity %v, not the probe's own", id)
		}
	}
}

func TestAvailableCleanupRetrySucceeds(t *testing.T) {
	p := newProbeScript(permissionStderr)
	if !scriptedKeychain(t, p).Available() {
		t.Fatal("Available() = false although the retried delete removed the probe item")
	}
	if p.sets != 1 || p.dels != 2 {
		t.Fatalf("runner saw %d sets and %d deletes, want 1 and 2", p.sets, p.dels)
	}
	if _, present := p.fake.items[availabilityProbeAccount]; present {
		t.Fatal("the probe item survived a probe that reported available")
	}
}

func TestAvailableCleanupNotFoundIsAbsence(t *testing.T) {
	p := newProbeScript(notFoundStderr)
	if !scriptedKeychain(t, p).Available() {
		t.Fatal("Available() = false although the delete reported a genuine not-found")
	}
	if p.dels != 1 {
		t.Fatalf("a genuine not-found was retried: %d deletes", p.dels)
	}
}

// TestAvailableCleanupPermissionIsNotAbsence also pins the retry count: an
// implementation that read the permission error as absence never retries,
// even when a later check happens to catch the surviving item.
func TestAvailableCleanupPermissionIsNotAbsence(t *testing.T) {
	p := newProbeScript(permissionStderr, permissionStderr, permissionStderr, permissionStderr)
	if dels := requireCleanupRefusal(t, scriptedKeychain(t, p), p); dels != 2 {
		t.Fatalf("a permission error was not retried (%d deletes): it was read as absence", dels)
	}
}

func TestAvailableCleanupSpoofedNotFoundIsNotAbsence(t *testing.T) {
	p := newProbeScript(spoofStderr, spoofStderr, spoofStderr, spoofStderr)
	if dels := requireCleanupRefusal(t, scriptedKeychain(t, p), p); dels != 2 {
		t.Fatalf("a denial quoting the not-found phrase was read as absence (%d deletes)", dels)
	}
}

// TestAvailableCleanupEveryDenialWordBeatsNotFound pins each entry of
// probeAbsent's denial list on its own: stderr that carries the not-found
// phrase AND that word is a refusal, never absence. The other tests hit
// only some words, so dropping any single entry would otherwise pass.
func TestAvailableCleanupEveryDenialWordBeatsNotFound(t *testing.T) {
	const phrase = "The specified item could not be found in the keychain."
	for _, word := range []string{"not allowed", "not permitted", "denied", "permission", "locked", "passphrase"} {
		t.Run(word, func(t *testing.T) {
			stderr := "security: SecKeychainItemDelete: " + word + ". " + phrase
			p := newProbeScript(stderr, stderr, stderr, stderr)
			if dels := requireCleanupRefusal(t, scriptedKeychain(t, p), p); dels != 2 {
				t.Fatalf("stderr %q was read as absence (%d deletes, want 2)", stderr, dels)
			}
		})
	}
}

func TestAvailableCleanupTransientErrorIsRefused(t *testing.T) {
	p := newProbeScript(transientStderr, transientStderr, transientStderr, transientStderr)
	if dels := requireCleanupRefusal(t, scriptedKeychain(t, p), p); dels != 2 {
		t.Fatalf("a transient delete failure ran %d deletes, want the one bounded retry", dels)
	}
}

func TestAvailableRefusesWhenProbeItemSurvivesCleanup(t *testing.T) {
	p := newProbeScript()
	p.keepItem = true
	requireCleanupRefusal(t, scriptedKeychain(t, p), p)
}

// TestAvailableCleanupPostDeleteFindErrorIsRefused pins the last line of
// removeProbe: the delete reported success, but the confirming find failed
// with an error that is not a not-found. Absence is unproven, so the probe
// must refuse (by identity and message) rather than report available.
func TestAvailableCleanupPostDeleteFindErrorIsRefused(t *testing.T) {
	p := newProbeScript()
	p.keepItem, p.findStderr = true, findDeniedStderr
	if dels := requireCleanupRefusal(t, scriptedKeychain(t, p), p); dels != 1 {
		t.Fatalf("a clean delete ran %d deletes, want 1 (the find, not the delete, failed)", dels)
	}
}

func TestAvailableHonoursCancellation(t *testing.T) {
	p := newProbeScript()
	if !scriptedKeychain(t, p).Available() {
		t.Fatal("Available() = false with a working security tool")
	}
	if p.noDeadline != 0 || len(p.deadlines) == 0 {
		t.Fatalf("runner saw %d calls without a deadline and %d with one", p.noDeadline, len(p.deadlines))
	}
	for _, dl := range p.deadlines {
		if left := time.Until(dl); left > 2*time.Second {
			t.Fatalf("probe context deadline is %v away, want at most 2s", left)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := newProbeScript()
	err := scriptedKeychain(t, cancelled).probe(ctx)
	if err == nil {
		t.Fatal("a cancelled probe reported available")
	}
	if cancelled.sets != 0 || len(cancelled.fake.items) != 0 {
		t.Fatalf("a cancelled probe still wrote: %d sets, items %v", cancelled.sets, cancelled.fake.items)
	}
}

// TestAvailableSetFailureStaysPlainUnavailable: a keychain that refuses the
// WRITE (locked, headless) wrote nothing, so it keeps the documented plain
// unavailable that sends SelectCustody to the file vault (R-14.270), even
// when its cleanup delete is refused too.
func TestAvailableSetFailureStaysPlainUnavailable(t *testing.T) {
	p := newProbeScript(lockedStderr, lockedStderr)
	p.fake.fail["add-generic-password"] = lockedStderr
	err := scriptedKeychain(t, p).probe(context.Background())
	if err == nil || err == ErrProbeCleanupFailed {
		t.Fatalf("probe error = %v, want a plain unavailable", err)
	}
}
