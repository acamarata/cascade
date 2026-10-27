// Purpose: SelectCustodyReadOnly reads without writing: the data directory's
//
//	digest is unchanged, an uninitialised vault is not created, and the fake
//	keychain runner sees no write verb.
//
// SPORT: internal/secrets SelectCustodyReadOnly tests (P1-SEC-23).

package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// dirDigest hashes every path, mode and content under dir.
func dirDigest(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		_, _ = h.Write([]byte(p + "|" + info.Mode().String() + "|"))
		if d.IsDir() {
			// A file created and removed again (a write probe) leaves no
			// entry but moves the directory's modification time.
			_, _ = h.Write([]byte(info.ModTime().String() + "|"))
		}
		if !d.IsDir() {
			raw, err := os.ReadFile(p) //nolint:gosec // test temp dir
			if err != nil {
				return err
			}
			_, _ = h.Write(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fakeKeychainLog records every external call and fails each one.
type fakeKeychainLog struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeKeychainLog) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	return nil, cascade.New(cascade.KindUnavailable, "fake keychain: unavailable")
}

func TestSelectCustodyReadOnlyWritesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fv, err := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := fv.Set(ctx, ApprovalKeyName, []byte("fake-stored-approval-key")); err != nil {
		t.Fatal(err)
	}
	before := dirDigest(t, dir)
	log := &fakeKeychainLog{}
	// The platform leg runs only where its backend shells out to the fake
	// runner (darwin); elsewhere the file vault is forced so no host secret
	// service is contacted.
	cfg := Config{Service: DefaultVaultService, Dir: dir, Runner: log.run, ForceFileVault: goruntime.GOOS != "darwin"}
	c, err := SelectCustodyReadOnly(cfg)
	if err != nil {
		t.Fatalf("SelectCustodyReadOnly: %v", err)
	}
	got, err := c.Get(ctx, ApprovalKeyName)
	if err != nil || !bytes.Equal(got, []byte("fake-stored-approval-key")) {
		t.Fatalf("Get = %q, %v; want the stored value", got, err)
	}
	_ = c.Available()
	_, _ = c.List(ctx)
	if !cascade.HasKind(c.Set(ctx, "x", []byte("y")), cascade.KindPermissionDenied) ||
		!cascade.HasKind(c.Delete(ctx, ApprovalKeyName), cascade.KindPermissionDenied) {
		t.Fatal("the read-only custody accepted a write")
	}
	if after := dirDigest(t, dir); after != before {
		t.Fatal("the data directory changed under a read-only selection")
	}
	for _, call := range log.calls {
		if strings.Contains(call, "add-") || strings.Contains(call, "delete-") || strings.Contains(call, "-U") {
			t.Fatalf("the keychain runner saw a write: %q", call)
		}
	}
	if goruntime.GOOS == "darwin" && len(log.calls) == 0 {
		t.Fatal("the darwin platform leg never consulted the runner; this test proves nothing there")
	}
}

func TestSelectCustodyReadOnlyCreatesNoVault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	c, err := SelectCustodyReadOnly(Config{Service: DefaultVaultService, Dir: dir, ForceFileVault: true})
	if err != nil {
		t.Fatalf("SelectCustodyReadOnly: %v", err)
	}
	if _, err := c.Get(context.Background(), ApprovalKeyName); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get on an uninitialised vault = %v, want KindNotFound", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the vault directory was created: %v", err)
	}
}
