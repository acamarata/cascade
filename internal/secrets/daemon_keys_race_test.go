// Purpose: two processes provisioning the same daemon key at once: the
//
//	cross-process lock makes the absence check and the create one critical
//	section, so exactly one key is written and both observe it. The window
//	between check and write is widened in each child, so a check-then-create
//	without the lock writes twice and the provisioners disagree.
//
// SPORT: internal/secrets ProvisionDaemonKey concurrency test (P1-SEC-23).

package secrets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// platformLikeCustody hides the file vault's create-if-absent.
type platformLikeCustody struct{ Custody }

const provisionChildEnv = "CASCADE_TEST_PROVISION_CHILD_DIR"

// provisionChild provisions over the shared vault directory and prints the
// public key it observed and whether it created it.
func provisionChild(dir string) {
	provisionYield = func() { time.Sleep(300 * time.Millisecond) }
	fv, err := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: filepath.Join(dir, "vault"), Passphrase: "fake-race-pass"})
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(2)
	}
	// A custody with no atomic create-if-absent, as a platform keystore is:
	// only the provisioning lock keeps the two processes from both writing.
	b, _ := NewBroker(platformLikeCustody{Custody: fv}, unelevatedRefusingGate{})
	for _, err := os.Stat(filepath.Join(dir, "go")); os.IsNotExist(err); _, err = os.Stat(filepath.Join(dir, "go")) {
		time.Sleep(5 * time.Millisecond)
	}
	_, pub, created, err := ProvisionDaemonKey(context.Background(), b.WithDaemonKeyLock(filepath.Join(dir, "lock", "daemon-key.lock")), ApprovalKeyName, rand.Reader)
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(3)
	}
	fmt.Printf("PUB %s %v\n", hex.EncodeToString(pub), created)
	os.Exit(0)
}

func TestProvisionDaemonKeyConcurrentProcessesCreateOnce(t *testing.T) {
	if dir := os.Getenv(provisionChildEnv); dir != "" {
		provisionChild(dir)
		return
	}
	dir := t.TempDir()
	outs := make([]string, 2)
	var wg sync.WaitGroup
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestProvisionDaemonKeyConcurrentProcessesCreateOnce$", "-test.count=1") //nolint:gosec // re-runs this test binary
			cmd.Env = append(os.Environ(), provisionChildEnv+"="+dir)
			out, _ := cmd.CombinedOutput()
			outs[i] = string(out)
		}(i)
	}
	time.Sleep(500 * time.Millisecond) // both children are waiting on the start file
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	pubs, created := map[string]bool{}, 0
	for _, out := range outs {
		var pub string
		var made bool
		line := out[strings.Index(out, "PUB "):]
		if _, err := fmt.Sscanf(line, "PUB %s %t", &pub, &made); err != nil {
			t.Fatalf("child output %q: %v", out, err)
		}
		pubs[pub] = true
		if made {
			created++
		}
	}
	fv, _ := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: filepath.Join(dir, "vault"), Passphrase: "fake-race-pass"})
	stored, err := fv.Get(context.Background(), ApprovalKeyName)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := daemonPublicKey(ApprovalKeyName, append([]byte(nil), stored...))
	if len(pubs) != 1 || created != 1 || !pubs[hex.EncodeToString(pub)] {
		t.Fatalf("provisioners saw %d keys, %d created; want one key, created once, equal to the stored key\n%s", len(pubs), created, strings.Join(outs, "\n"))
	}
}

func TestProvisionDaemonKeyRefusesWithoutALock(t *testing.T) {
	b, cc := newCountingBroker(t)
	b.keyLockPath = ""
	if _, _, _, err := ProvisionDaemonKey(context.Background(), b, ApprovalKeyName, rand.Reader); err == nil || cc.sets.Load() != 0 {
		t.Fatalf("provisioning without a lock = %v (writes %d), want a refusal and no write", err, cc.sets.Load())
	}
}

// entropyBarrier pauses the first read of randomness (the vault save's salt)
// until released, holding a writer between its load and its save.
type entropyBarrier struct {
	once             sync.Once
	entered, release chan struct{}
}

func (r *entropyBarrier) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return rand.Read(p)
}

// vaultLockContended returns a channel closed the first time any writer
// finds the file-vault lock held.
func vaultLockContended(t *testing.T) <-chan struct{} {
	t.Helper()
	contended := make(chan struct{})
	var once sync.Once
	lockContended = func(path string) {
		if strings.HasSuffix(path, fileVaultLockName) {
			once.Do(func() { close(contended) })
		}
	}
	t.Cleanup(func() { lockContended = func(string) {} })
	return contended
}

// TestOrdinarySetCannotEraseProvisionedKey: an ordinary Set paused between
// its load and its save while the key is provisioned (the reviewer's
// repro, reordered so a correct lock does not deadlock it). Every file-vault
// mutation holds one lock around load-modify-save, so both entries survive.
func TestOrdinarySetCannotEraseProvisionedKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	barrier := &entropyBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	ordinary, err := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: dir, Passphrase: "fake-review-pass", Rand: barrier})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: dir, Passphrase: "fake-review-pass"})
	if err != nil {
		t.Fatal(err)
	}
	ob, _ := NewBroker(ordinary, nil)
	db, _ := NewBroker(daemon, nil)
	setDone, provDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := ob.Set(ctx, "ORDINARY_KEY", []byte("fake-ordinary"), SetUpdate); setDone <- err }()
	select {
	case <-barrier.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the ordinary Set never reached its save")
	}
	contended := vaultLockContended(t)
	go func() {
		_, _, _, err := ProvisionDaemonKey(ctx, db.WithDaemonKeyLock(filepath.Join(dir, "daemon-key.lock")), ApprovalKeyName, rand.Reader)
		provDone <- err
	}()
	// Release the paused Set only once provisioning is provably blocked on
	// the vault lock, or (no lock) has already written: no timing involved.
	var provErr error
	provFinished := false
	select {
	case <-contended:
	case provErr = <-provDone:
		provFinished = true
	}
	close(barrier.release)
	if err := <-setDone; err != nil {
		t.Fatalf("ordinary Set: %v", err)
	}
	if !provFinished {
		provErr = <-provDone
	}
	if provErr != nil {
		t.Fatalf("provision: %v", provErr)
	}
	key, kerr := daemon.Get(ctx, ApprovalKeyName)
	other, oerr := daemon.Get(ctx, "ORDINARY_KEY")
	if kerr != nil || len(key) != 64 || oerr != nil || string(other) != "fake-ordinary" {
		t.Fatalf("after concurrent writes: key %d bytes (%v), ordinary %q (%v); want both", len(key), kerr, other, oerr)
	}
}

// TestFreshVaultKeyCreatedOnceUnderRace: two writers opening a fresh vault
// (no passphrase) at the same moment must end up with one key file and the
// same passphrase; otherwise one of them encrypts with a key nobody can
// read again.
func TestFreshVaultKeyCreatedOnceUnderRace(t *testing.T) {
	for i := 0; i < 200; i++ {
		dir := t.TempDir()
		start := make(chan struct{})
		got := make([]*fileVaultCustody, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for j := range got {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				<-start
				got[j], errs[j] = newFileVaultCustody(Config{Service: DefaultVaultService, Dir: dir})
			}(j)
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("iteration %d: %v / %v", i, errs[0], errs[1])
		}
		onDisk, err := os.ReadFile(filepath.Join(dir, fileVaultKeyName)) //nolint:gosec // test temp dir
		if err != nil || got[0].passphrase != got[1].passphrase || got[0].passphrase != string(onDisk) {
			t.Fatalf("iteration %d: the two openers hold different vault keys (or not the one on disk): %v", i, err)
		}
	}
}
