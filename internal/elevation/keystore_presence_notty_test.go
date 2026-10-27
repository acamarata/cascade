//go:build linux

// Purpose: presence tests that need the real verifier (internal/rpc imports
//
//	this package), a real pty, a subprocess with no terminal, rotation, audit
//	and custody ranking.
//
// SPORT: internal/elevation presence tests/ADDED (P1-SEC-25).
package elevation_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	helperDirEnv = "CASCADE_PRESENCE_HELPER_DIR"
	passA        = "correct horse battery"
	passB        = "second staple passphrase"
)

func noEnv(string) string { return "" }

func clock() runtime.Clock { return runtime.NewFixedClock(time.Unix(1_700_000_000, 0)) }

func opener(p *elevation.ScriptedPrompter) func() (elevation.PresencePrompter, error) {
	return func() (elevation.PresencePrompter, error) { return p, nil }
}

func enrol(t *testing.T, dir string, a *elevation.PresenceAudit, answers ...string) (string, error) {
	t.Helper()
	t.Cleanup(elevation.SetPresenceCostForTest(32768))
	return elevation.EnrollPresenceForTest(dir, opener(elevation.NewScriptedPrompter(answers...)), noEnv,
		elevation.NewFileBackend(dir), clock(), a)
}

func readKey(dir string) []byte {
	raw, _ := os.ReadFile(dir + "/elevation/presence.key")
	return raw
}

func signed(t *testing.T, ks elevation.ElevationKeystore, led *rpc.NonceLedger, now time.Time, hash string) (rpc.Attestation, []byte) {
	t.Helper()
	pub, _ := ks.PubKeyB64()
	fp, _ := elevation.Fingerprint(pub)
	nonce, err := led.Issue("vault.get", hash)
	if err != nil {
		t.Fatal(err)
	}
	att := rpc.Attestation{RequestID: "req-" + nonce[:6], ActionHash: hash, Method: "vault.get", Nonce: nonce, PubkeyFingerprint: fp,
		IssuedUnix: now.Unix(), ExpUnix: now.Add(5 * time.Minute).Unix()}
	sig, err := ks.Sign(rpc.AttestationSignedBytes(att))
	if err != nil {
		t.Fatal(err)
	}
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	return att, sig
}

func TestPresenceSignRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := enrol(t, dir, &elevation.PresenceAudit{}, passA, passA); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dir + "/elevation/presence.key"); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("presence.key stat = %v, %v; want mode 0600", st, err)
	}
	signer := elevation.NewScriptedPrompter(passA, passA)
	ks := elevation.NewPresenceKeystoreForTest(dir, opener(signer), noEnv)
	clk := clock()
	led := rpc.NewNonceLedger(clk)
	pub, _ := ks.PubKeyB64()
	raw, _ := base64.StdEncoding.DecodeString(pub)
	fp, _ := elevation.Fingerprint(pub)
	trust := rpc.MapTrustStore{fp: ed25519.PublicKey(raw)}
	for i, hash := range []string{"hash-one", "hash-two"} {
		att, _ := signed(t, ks, led, clk.Now(), hash)
		if err := rpc.VerifyAttestation(att, trust, led, "vault.get", hash, clk.Now()); err != nil {
			t.Fatalf("sign %d: the real verifier rejected the attestation: %v", i, err)
		}
		if signer.Reads() != i+1 {
			t.Fatalf("after sign %d the passphrase was read %d times; want %d (no caching)", i, signer.Reads(), i+1)
		}
	}
	att, _ := signed(t, elevation.NewPresenceKeystoreForTest(dir, opener(elevation.NewScriptedPrompter(passA)), noEnv), led, clk.Now(), "hash-3")
	att.ActionHash = "tampered"
	if rpc.VerifyAttestation(att, trust, led, "vault.get", "tampered", clk.Now()) == nil {
		t.Fatal("the verifier accepted a tampered attestation: the round trip proves nothing")
	}
}

// TestHelperPresence is the child of the terminal tests: it signs through the production
// prompter and prints the result. It does nothing unless a parent test launched it.
func TestHelperPresence(_ *testing.T) {
	if dir := os.Getenv(helperDirEnv); dir != "" {
		sig, err := elevation.NewProductionPresenceForTest(dir).Sign([]byte("payload"))
		fmt.Printf("RESULT sig=%d err=%v\n", len(sig), err)
	}
}

func TestPresenceRefusesWithoutControllingTTY(t *testing.T) {
	dir := t.TempDir()
	if _, err := enrol(t, dir, &elevation.PresenceAudit{}, passA, passA); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPresence$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperDirEnv+"="+dir, "CASCADE_PRESENCE_PASSPHRASE="+passA, "CASCADE_ELEVATION_PASSPHRASE="+passA)
	cmd.Stdin = strings.NewReader(passA + "\n")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("child failed: %v\n%s", err, out.String())
	}
	if want := "RESULT sig=0 err=" + elevation.ErrAuthFailed(nil).Error(); !strings.Contains(out.String(), want) {
		t.Fatalf("no controlling terminal must refuse with %q and sign nothing; child said:\n%s", want, out.String())
	}
}

func TestPresenceReenrolRequiresOldPassphrase(t *testing.T) {
	dir := t.TempDir()
	a := &elevation.PresenceAudit{}
	fp1, err := enrol(t, dir, a, passA, passA)
	if err != nil {
		t.Fatal(err)
	}
	before := readKey(dir)
	if _, err := enrol(t, dir, a, "wrong horse battery!", passB, passB); err == nil || !strings.HasPrefix(err.Error(), elevation.ErrAuthFailed(nil).Error()) {
		t.Fatalf("re-enrol with a wrong old passphrase = %v, want ErrAuthFailed", err)
	}
	if !bytes.Equal(before, readKey(dir)) || len(a.Begun) != 1 {
		t.Fatalf("a refused rotation changed the key file or wrote an audit record (records=%d)", len(a.Begun))
	}
	fp2, err := enrol(t, dir, a, passA, passB, passB)
	if err != nil {
		t.Fatalf("rotation with the right old passphrase: %v", err)
	}
	if fp2 == fp1 || bytes.Equal(before, readKey(dir)) {
		t.Fatal("the key did not rotate")
	}
	if got, found, err := elevation.PresenceFingerprint(dir); err != nil || !found || got != fp2 {
		t.Fatalf("PresenceFingerprint = %q,%v,%v; want %q", got, found, err, fp2)
	}
	if len(a.Begun) != 2 || a.Begun[1] != "rotation:"+fp2 || a.Begun[0] != "fresh:"+fp1 || len(a.Finished) != 2 || a.Finished[1] != nil {
		t.Fatalf("audit records = %v finished=%v; want one fresh and one rotation naming the new fingerprint", a.Begun, a.Finished)
	}
	rec, ok, _ := elevation.NewFileBackend(dir).Load()
	if !ok || rec.FingerprintSHA256 != fp2 {
		t.Fatalf("trust record = %+v, want the rotated fingerprint", rec)
	}
	if _, err := elevation.NewPresenceKeystoreForTest(dir, opener(elevation.NewScriptedPrompter(passA)), noEnv).Sign([]byte("p")); err == nil {
		t.Fatal("the old passphrase still signs after rotation")
	}
	if _, err := elevation.NewPresenceKeystoreForTest(dir, opener(elevation.NewScriptedPrompter(passB)), noEnv).Sign([]byte("p")); err != nil {
		t.Fatalf("the new passphrase must sign: %v", err)
	}
}

func TestPresenceEnrolAuditFailureRefuses(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "audit log unavailable")
	dir := t.TempDir()
	if _, err := enrol(t, dir, &elevation.PresenceAudit{BeginErr: boom}, passA, passA); err == nil || !strings.Contains(err.Error(), "audit log unavailable") {
		t.Fatalf("enrol with a failing audit log = %v, want the audit error", err)
	}
	if _, err := os.Stat(dir + "/elevation/presence.key"); !os.IsNotExist(err) {
		t.Fatalf("presence.key exists after a refused enrolment: %v", err)
	}
	if _, ok, _ := elevation.NewFileBackend(dir).Load(); ok {
		t.Fatal("a trust record was written for a refused enrolment")
	}
	for name, answers := range map[string][]string{"short": {"short", "short"}, "mismatch": {passA, passB}} {
		if _, err := enrol(t, t.TempDir(), &elevation.PresenceAudit{}, answers...); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s passphrase = %v, want KindInvalidInput", name, err)
		}
	}
	if _, err := enrol(t, dir, &elevation.PresenceAudit{}, passA, passA); err != nil {
		t.Fatalf("control: with a working audit log the same enrolment succeeds: %v", err)
	}
}

func TestSelectPrefersPlatformOverPresence(t *testing.T) {
	dir := t.TempDir()
	if _, err := enrol(t, dir, &elevation.PresenceAudit{}, passA, passA); err != nil {
		t.Fatal(err)
	}
	platform := elevation.CustodySource{Tier: elevation.CustodyPlatform, Name: "platform-stand-in", Open: func(d string) (elevation.ElevationKeystore, bool) {
		return elevation.NewPresenceKeystoreForTest(d, opener(elevation.NewScriptedPrompter()), noEnv), true
	}}
	both := elevation.Selector{DataDir: dir, Sources: append(elevation.DefaultSources(), platform)}
	if c := both.Select(); c.Tier() != elevation.CustodyPlatform || c.Source() != "platform-stand-in" {
		t.Fatalf("with both sources selected %s/%s, want the platform source", c.Tier(), c.Source())
	}
	only := elevation.Selector{DataDir: dir, Sources: elevation.DefaultSources()}
	if c := only.Select(); c.Tier() != elevation.CustodyPresence || !c.Tier().SatisfiesElevation() {
		t.Fatalf("presence alone selected %s; want presence, which satisfies elevation", c.Tier())
	}
	if c := (elevation.Selector{DataDir: t.TempDir(), Sources: elevation.DefaultSources()}).Select(); c.Tier() == elevation.CustodyPresence {
		t.Fatal("presence selected with no presence.key enrolled")
	}
}

func TestCustodyReportPresenceTier(t *testing.T) {
	dir := t.TempDir()
	if _, err := enrol(t, dir, &elevation.PresenceAudit{}, passA, passA); err != nil {
		t.Fatal(err)
	}
	st := elevation.CustodyReport(elevation.Selector{DataDir: dir, Sources: elevation.DefaultSources()}, elevation.NewFileBackend(dir), clock())
	if st.Tier != elevation.CustodyPresence || st.Source != "presence-passphrase" || !st.Enrolled || !st.Bound {
		t.Fatalf("custody report = %+v, want tier presence, enrolled and bound", st)
	}
}

// TestRealTerminalPromptsWithoutEcho drives the production prompter through a real
// pty: the prompt appears, the typed passphrase is not echoed, and the key signs.
func TestRealTerminalPromptsWithoutEcho(t *testing.T) {
	dir := t.TempDir()
	if _, err := enrol(t, dir, &elevation.PresenceAudit{}, passA, passA); err != nil {
		t.Fatal(err)
	}
	master, slave := elevation.OpenPtyForTest(t)
	defer func() { _ = master.Close() }()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPresence$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperDirEnv+"="+dir)
	cmd.Stdin = slave
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	stop := time.AfterFunc(30*time.Second, func() { _ = cmd.Process.Kill() })
	defer stop.Stop()
	var screen bytes.Buffer
	for buf := make([]byte, 256); !bytes.Contains(screen.Bytes(), []byte("passphrase: ")); {
		n, err := master.Read(buf)
		screen.Write(buf[:n])
		if err != nil {
			t.Fatalf("the child never prompted: %v (%q)", err, screen.String())
		}
	}
	_, _ = io.WriteString(master, passA+"\n")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v\n%s", err, out.String())
	}
	rest, _ := io.ReadAll(master) // EIO once the slave is gone; keeps what was echoed
	screen.Write(rest)
	if bytes.Contains(screen.Bytes(), []byte(passA)) {
		t.Fatalf("the passphrase was echoed to the terminal: %q", screen.String())
	}
	if !strings.Contains(out.String(), "RESULT sig=64 err=<nil>") {
		t.Fatalf("a correct passphrase on a real terminal did not sign: %s", out.String())
	}
}
