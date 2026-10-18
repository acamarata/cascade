//go:build !windows

// Purpose: tests for how the helper keystore treats the helper's answers
//
//	(typed failures, malformed, oversize and wrong-shaped replies, runner
//	errors) through an injected runner; the wire exec path itself is covered
//	by keystore_helper_test.go.
//
// SPORT: internal/elevation helper response tests/ADDED (P1-SHIP-15).
package elevation

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func cannedRun(line string, err error) helperRunner {
	return func(_ context.Context, _ string, _ []byte, _ time.Duration, vet func(int) error) ([]byte, error) {
		if verr := vet(1); verr != nil {
			return nil, verr
		}
		return []byte(line), err
	}
}

func cannedKS(t *testing.T, run helperRunner) ElevationKeystore {
	t.Helper()
	dir, exe := install(t, false)
	writeFileForTest(t, filepath.Join(dir, HelperName))
	return newHelperKeystoreWith(func() (string, error) { return exe, nil }, run, OKVerifier{})
}

func b64(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }

func TestHelperKeystoreAnswers(t *testing.T) {
	status := `{"v":1,"ok":true,"available":true,"tier":"os-keychain","cgo":true}`
	ks := cannedKS(t, cannedRun(status, nil))
	if !ks.IsAvailable() || ks.Tier() != TierOSKeychain {
		t.Fatal("a good status was not accepted")
	}
	for _, bad := range []struct {
		reply     string
		available bool // what IsAvailable must say
	}{
		{`{"v":1,"ok":true,"available":true,"tier":"os-keychain","cgo":false}`, false},
		{`{"v":1,"ok":true,"available":false,"tier":"os-keychain","cgo":true}`, false},
		{`{"v":1,"ok":true,"available":true,"tier":"file","cgo":true}`, true},
	} {
		ks := cannedKS(t, cannedRun(bad.reply, nil))
		if ks.IsAvailable() != bad.available || ks.Tier() != TierUnavailable {
			t.Fatalf("status %s: available=%v tier=%v", bad.reply, ks.IsAvailable(), ks.Tier())
		}
	}
	if err := cannedKS(t, cannedRun(`{"v":1,"ok":true}`, nil)).GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if pub, err := cannedKS(t, cannedRun(`{"v":1,"ok":true,"pub_b64":"`+b64(32)+`"}`, nil)).PubKeyB64(); err != nil || pub != b64(32) {
		t.Fatalf("PubKeyB64 = %q, %v", pub, err)
	}
	if sig, err := cannedKS(t, cannedRun(`{"v":1,"ok":true,"sig_b64":"`+b64(64)+`"}`, nil)).Sign([]byte("p")); err != nil || len(sig) != 64 {
		t.Fatalf("Sign = %d bytes, %v", len(sig), err)
	}
}

func TestHelperKeystoreRefusesBadAnswers(t *testing.T) {
	cases := map[string]struct {
		reply string
		err   error
		call  func(ElevationKeystore) error
		kind  cascade.Kind
	}{
		"typed failure":      {`{"v":1,"ok":false,"kind":"permission-denied","message":"no"}`, nil, signErr, cascade.KindPermissionDenied},
		"unknown kind":       {`{"v":1,"ok":false,"kind":"bogus","message":"x"}`, nil, signErr, cascade.KindUnavailable},
		"conflict":           {`{"v":1,"ok":false,"kind":"conflict","message":"enrolled"}`, nil, genErr, cascade.KindConflict},
		"malformed json":     {`not json`, nil, signErr, cascade.KindUnavailable},
		"wrong version":      {`{"v":2,"ok":true,"sig_b64":"` + b64(64) + `"}`, nil, signErr, cascade.KindUnavailable},
		"oversize reply":     {`{"v":1,"ok":true,"sig_b64":"` + b64(64) + `"}` + strings.Repeat(" ", helperMaxLine), nil, signErr, cascade.KindUnavailable},
		"runner error":       {``, errors.New("boom"), signErr, cascade.KindUnavailable},
		"short signature":    {`{"v":1,"ok":true,"sig_b64":"` + b64(10) + `"}`, nil, signErr, cascade.KindIntegrity},
		"bad signature b64":  {`{"v":1,"ok":true,"sig_b64":"!!"}`, nil, signErr, cascade.KindIntegrity},
		"short public key":   {`{"v":1,"ok":true,"pub_b64":"` + b64(5) + `"}`, nil, pubErr, cascade.KindIntegrity},
		"failed public key":  {`{"v":1,"ok":false,"kind":"not-found","message":"none"}`, nil, pubErr, cascade.KindNotFound},
		"failed generate":    {`{"v":1,"ok":false,"kind":"unavailable","message":"x"}`, nil, genErr, cascade.KindUnavailable},
		"oversize payload":   {`{"v":1,"ok":true,"sig_b64":"` + b64(64) + `"}`, nil, bigSignErr, cascade.KindInvalidInput},
		"control characters": {"{\"v\":1,\"ok\":false,\"kind\":\"permission-denied\",\"message\":\"a\\u001b[31mb\"}", nil, signErr, cascade.KindPermissionDenied},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.call(cannedKS(t, cannedRun(c.reply, c.err)))
			wantHelperKind(t, err, c.kind)
			if strings.ContainsAny(err.Error(), "\x1b") {
				t.Fatalf("control bytes survived into the error: %q", err.Error())
			}
		})
	}
}

func signErr(ks ElevationKeystore) error { _, err := ks.Sign([]byte("p")); return err }
func bigSignErr(ks ElevationKeystore) error {
	_, err := ks.Sign(make([]byte, helperMaxPayload+1))
	return err
}
func pubErr(ks ElevationKeystore) error { _, err := ks.PubKeyB64(); return err }
func genErr(ks ElevationKeystore) error { return ks.GenerateKey() }

func TestHelperKeystoreWithoutRunnerIsUnavailable(t *testing.T) {
	dir, exe := install(t, false)
	writeFileForTest(t, filepath.Join(dir, HelperName))
	ks := newHelperKeystore(func() (string, error) { return exe, nil }, nil)
	if ks.IsAvailable() {
		t.Fatal("a keystore with no runner reported available")
	}
	wantHelperKind(t, signErr(ks), cascade.KindUnavailable)
	bad := newHelperKeystoreWith(func() (string, error) { return filepath.Join(dir, "gone"), nil }, cannedRun("", nil), OKVerifier{})
	wantHelperKind(t, signErr(bad), cascade.KindUnavailable)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}

func TestHelperKeystoreRefusesRelativeExecutable(t *testing.T) {
	// A cascade file and a valid helper sibling sit in the working directory,
	// so only the absolute-path rule can refuse the relative name.
	dir, _ := install(t, false)
	writeFileForTest(t, filepath.Join(dir, HelperName))
	t.Chdir(dir)
	old := lsACL // darwin: a relative path would fail the ACL listing too; leave only the absolute-path rule
	t.Cleanup(func() { lsACL = old })
	lsACL = func(string) ([]byte, error) { return nil, nil }
	ks := newHelperKeystoreWith(func() (string, error) { return "cascade", nil }, cannedRun(`{"v":1,"ok":true,"available":true,"tier":"os-keychain","cgo":true}`, nil), OKVerifier{})
	if ks.IsAvailable() {
		t.Fatal("a relative executable path resolved a helper")
	}
}

func TestHelperKeystoreIgnoresAConnectionFromAnotherProcess(t *testing.T) {
	dir, exe := install(t, true)
	setMode(t, dir, "keeper")
	pub, err := newKS(exe, OKVerifier{}).PubKeyB64()
	if err != nil {
		t.Fatalf("PubKeyB64: %v", err)
	}
	want := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed([]byte("cascade-fake-helper-test-seed-00")).Public().(ed25519.PublicKey))
	if pub != want {
		t.Fatalf("accepted a key from a process other than the verified child: %q", pub)
	}
	b, err := os.ReadFile(filepath.Join(dir, "keeperpid"))
	if err != nil {
		t.Fatalf("the descendant never ran: %v", err)
	}
	keeper, _ := strconv.Atoi(string(b))
	for end := time.Now().Add(3 * time.Second); syscall.Kill(keeper, 0) == nil && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(keeper, 0) == nil {
		t.Fatalf("descendant %d outlived the exchange", keeper)
	}
}

func TestHelperKeystoreRefusesAPreWrittenAnswer(t *testing.T) {
	dir, exe := install(t, true)
	setMode(t, dir, "prewrite")
	// The delay stands in for codesign's run time: the forged line is in the
	// socket long before the check on the running child returns.
	ks := newKS(exe, &scriptVerifier{pidDelay: 500 * time.Millisecond})
	if ks.IsAvailable() {
		t.Fatal("an answer written before the request was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "request")); !os.IsNotExist(err) {
		t.Fatalf("the request reached a child that had already written (stat err %v)", err)
	}
}

func TestHelperKeystoreOpensNoListenerBeforeVerification(t *testing.T) {
	_, exe := install(t, true)
	tmp, err := os.MkdirTemp("", "ce")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("TMPDIR", tmp)
	if _, err := os.ReadDir(tmp); err != nil {
		t.Fatal(err)
	}
	var during []string
	var listErr error
	v := &scriptVerifier{atPID: func() {
		entries, err := os.ReadDir(tmp)
		listErr = err
		for _, e := range entries {
			during = append(during, e.Name())
		}
	}}
	if _, err := newKS(exe, v).Sign([]byte("payload")); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(v.pidChecked) == 0 {
		t.Fatal("the running child was never verified")
	}
	if listErr != nil || len(during) != 0 {
		t.Fatalf("a listener existed (or could not be listed: %v) while the running child was being verified: %v", listErr, during)
	}
	left, err := os.ReadDir(tmp)
	if err != nil || len(left) != 0 {
		t.Fatalf("the exchange left %d entries behind (list error %v)", len(left), err)
	}
}
