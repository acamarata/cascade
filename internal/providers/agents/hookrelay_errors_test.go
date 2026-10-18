//go:build !windows

package agents

// Purpose: the hook relay's failure paths: bad hook input, a malformed
//   callback file, a driver that refuses or is gone, the Seq counter's
//   token key and lock, and a symlinked counter.
// Constraints: see hookbridge_test.go (the rig).
// SPORT: internal.providers.agents/ADD (P1-AGT-14).

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestHookRelayRefusesBadHookInput(t *testing.T) {
	r := newRig(t)
	post := r.hook("posttooluse-read.json")
	cases := map[string]struct {
		cwd, event string
		payload    []byte
	}{
		"event differs from the payload": {r.root, HookPreToolUse, post},
		"not JSON":                       {r.root, HookPostToolUse, []byte("not json")},
		"empty payload":                  {r.root, HookPostToolUse, nil},
		"unsupported event":              {r.root, "SessionEnd", bytes.ReplaceAll(post, []byte("PostToolUse"), []byte("SessionEnd"))},
		"relative cwd":                   {"pkg", HookPostToolUse, post},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := relayOnce(t, c.cwd, c.event, c.payload); kindOf(err) != cascade.KindInvalidInput {
				t.Fatalf("got %v, want an invalid-input refusal", err)
			}
		})
	}
	if r.connected() {
		t.Fatal("a bad hook payload reached the driver")
	}
}

func TestHookRelayRefusesMalformedCallbackFile(t *testing.T) {
	tok := strings.Repeat("ab", 16)
	bodies := map[string]string{
		"not JSON":          `nope`,
		"unknown field":     `{"socket":"/x/s","token":"` + tok + `","extra":1}`,
		"relative socket":   `{"socket":"s","token":"` + tok + `"}`,
		"short token":       `{"socket":"/x/s","token":"abcd"}`,
		"upper-case token":  `{"socket":"/x/s","token":"` + strings.ToUpper(tok) + `"}`,
		"non-hex token":     `{"socket":"/x/s","token":"` + strings.Repeat("zz", 16) + `"}`,
		"zero token":        `{"socket":"/x/s","token":"` + strings.Repeat("00", 16) + `"}`,
		"two JSON values":   `{"socket":"/x/s","token":"` + tok + `"} {}`,
		"oversize callback": `{"socket":"/` + strings.Repeat("a", 5000) + `","token":"` + tok + `"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			path := filepath.Join(r.root, ".claude", provider.CallbackFileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := relayOnce(t, r.root, HookStop, r.hook("stop.json")); kindOf(err) != cascade.KindPermissionDenied {
				t.Fatalf("got %v, want a permission-denied refusal", err)
			}
			if r.connected() {
				t.Fatal("a frame was sent from a malformed callback file")
			}
		})
	}
}

func TestHookRelayDriverRefusalAndAbsence(t *testing.T) {
	t.Run("the driver refuses the frame", func(t *testing.T) {
		r := newRig(t)
		r.ack = "refused order"
		r.serve(1)
		err := relayOnce(t, r.root, HookStop, r.hook("stop.json"))
		r.wait()
		if kindOf(err) != cascade.KindPermissionDenied || !strings.Contains(err.Error(), "order") {
			t.Fatalf("got %v, want a permission-denied refusal naming the driver's reason", err)
		}
	})
	t.Run("the driver is gone", func(t *testing.T) {
		r := newRig(t)
		if err := syscall.Close(r.fd); err != nil {
			t.Fatal(err)
		}
		if err := relayOnce(t, r.root, HookStop, r.hook("stop.json")); kindOf(err) != cascade.KindUnavailable {
			t.Fatalf("got %v, want an unavailable error", err)
		}
	})
	t.Run("the driver answers nothing useful", func(t *testing.T) {
		r := newRig(t)
		r.ack = "huh"
		r.serve(1)
		err := relayOnce(t, r.root, HookStop, r.hook("stop.json"))
		r.wait()
		if kindOf(err) != cascade.KindUnavailable {
			t.Fatalf("got %v, want an unavailable error", err)
		}
	})
}

func TestHookRelaySeqCounterIsKeyedByToken(t *testing.T) {
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "seq"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	seq := func(tok string) uint64 {
		n, err := nextSeq(f, tok)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, want := range []uint64{1, 2, 3} {
		if got := seq("aa"); got != want {
			t.Fatalf("token aa: Seq %d, want %d", got, want)
		}
	}
	if got := seq("bb"); got != 1 {
		t.Fatalf("a new token must restart at 1, got %d", got)
	}
	if err := f.Truncate(0); err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteAt([]byte("bb not-a-number\n"), 0)
	if got := seq("bb"); got != 1 {
		t.Fatalf("a corrupt counter must restart at 1, got %d", got)
	}
}

func TestHookRelayWaitsForTheSeqLockAndTimesOut(t *testing.T) {
	r := newRig(t)
	counterPath := filepath.Join(filepath.Dir(r.sock), counterFileName)
	held, err := openCounter(counterPath, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	if ok, err := tryLockFile(held); err != nil || !ok {
		t.Fatalf("test lock: %v %v", ok, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = Relay(ctx, r.root, HookStop, bytes.NewReader(r.hook("stop.json")), os.Getuid())
	if kindOf(err) != cascade.KindTimeout {
		t.Fatalf("got %v, want a timeout while the counter is locked", err)
	}
	unlockFile(held)
	r.serve(1)
	if err := relayOnce(t, r.root, HookStop, r.hook("stop.json")); err != nil {
		t.Fatalf("after the lock is released: %v", err)
	}
	r.wait()
}

func TestHookRelayRefusesSymlinkedCounter(t *testing.T) {
	r := newRig(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(filepath.Dir(r.sock), counterFileName)); err != nil {
		t.Skip(err)
	}
	if err := relayOnce(t, r.root, HookStop, r.hook("stop.json")); kindOf(err) != cascade.KindPermissionDenied {
		t.Fatalf("got %v, want a permission-denied refusal", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "x" {
		t.Fatalf("the counter was written through a symlink: %q", b)
	}
	if r.connected() {
		t.Fatal("a frame was sent with an untrusted counter")
	}
}
