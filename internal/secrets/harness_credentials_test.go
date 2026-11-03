package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// hcCanary is assembled at run time so no token-shaped literal sits in the tree.
var (
	hcCanary = strings.Join([]string{"hc", "canary", "7f3a9e"}, "-")
	hcEpoch  = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
)

// hcCustody fails Set for one slot suffix and otherwise is memCustody.
type hcCustody struct {
	*memCustody
	failSuffix string
}

func (c hcCustody) Set(ctx context.Context, n string, v []byte) error {
	if c.failSuffix != "" && strings.HasSuffix(n, c.failSuffix) {
		return cascade.New(cascade.KindUnavailable, "mem: injected set failure")
	}
	return c.memCustody.Set(ctx, n, v)
}

// hcSetup isolates HOME/USERPROFILE/CASCADE_HOME and returns a config dir.
func hcSetup(t *testing.T) string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX mode bits and uids are not observable on windows")
	}
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, home)
	}
	return home
}

func hcFileStore() fileStore {
	return fileStore{uid: os.Getuid(), owner: harnessFileOwner, writeFn: (*os.File).Write, rename: os.Rename}
}

func hcCreds(store entryStore, c Custody) *harnessCredentials {
	return &harnessCredentials{store: store, custody: c, clock: runtime.NewFixedClock(hcEpoch)}
}

// hcEntry builds a valid entry; a NewEntry failure surfaces as an empty
// entry, which the format test's positive check rejects.
func hcEntry(tag string) Entry {
	e, _ := NewEntry([]byte(`{"accessToken":"` + hcCanary + `-` + tag + `","refreshToken":"r-` + tag + `"}`))
	return e
}

func hcWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	if err := errors.Join(os.WriteFile(path, data, mode), os.Chmod(path, mode)); err != nil {
		t.Fatal(err)
	}
}

func hcOnlyEntryLeft(t *testing.T, dir string) {
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != ".credentials.json" {
		t.Fatalf("dir holds %d entries, want only the credentials file", len(ents))
	}
}

func hcKind(t *testing.T, err error, want cascade.Kind, msg string) {
	t.Helper()
	kind, ok := cascade.KindOf(err)
	if !ok || kind != want || (msg != "" && err.Error() != want.String()+": "+msg) {
		t.Fatalf("err kind %v (%v), message %q; want %v %q", kind, ok, err, want, msg)
	}
	if strings.Contains(err.Error(), hcCanary) {
		t.Fatal("error text carries entry bytes")
	}
}

func TestDurableWriteNeverBlanksEntry(t *testing.T) {
	failures := map[string]struct {
		kind   cascade.Kind
		inject func(*fileStore, *hcCustody)
	}{
		"partial write": {cascade.KindUnavailable, func(f *fileStore, _ *hcCustody) {
			f.writeFn = func(fh *os.File, b []byte) (int, error) {
				n, _ := fh.Write(b[:len(b)/2])
				return n, errors.New("disk full")
			}
		}},
		"rename fails":    {cascade.KindUnavailable, func(f *fileStore, _ *hcCustody) { f.rename = func(string, string) error { return errors.New("EXDEV") } }},
		"staging fails":   {cascade.KindUnavailable, func(_ *fileStore, c *hcCustody) { c.failSuffix = ".staged" }},
		"rollback fails":  {cascade.KindUnavailable, func(_ *fileStore, c *hcCustody) { c.failSuffix = ".rollback" }},
		"empty entry":     {cascade.KindInvalidInput, func(*fileStore, *hcCustody) {}},
		"wider live mode": {cascade.KindPermissionDenied, func(*fileStore, *hcCustody) {}},
	}
	for name, tc := range failures {
		t.Run(name, func(t *testing.T) {
			dir := hcSetup(t)
			path := filepath.Join(dir, ".credentials.json")
			old := hcEntry("old").Bytes()
			hcWrite(t, path, old, map[bool]os.FileMode{true: 0o644, false: 0o600}[name == "wider live mode"])
			store, cust := hcFileStore(), &hcCustody{memCustody: newMemCustody()}
			tc.inject(&store, cust)
			next := map[bool]Entry{true: {}, false: hcEntry("new")}[name == "empty entry"]
			hcKind(t, hcCreds(store, cust).WriteDurable(context.Background(), dir, next), tc.kind, "")
			if got, _ := os.ReadFile(path); !bytes.Equal(got, old) {
				t.Fatal("live entry changed after a failed write")
			}
			hcOnlyEntryLeft(t, dir)
		})
	}
	hcCheckVerifiedWrite(t)
}

func TestHarnessCredentialsLinuxAtomicWrite(t *testing.T) {
	dir := hcSetup(t)
	path := filepath.Join(dir, ".credentials.json")
	old, next := hcEntry("old"), hcEntry("new")
	hcWrite(t, path, old.Bytes(), 0o600)
	store := hcFileStore()
	store.writeFn = func(fh *os.File, b []byte) (int, error) {
		fi, err := fh.Stat()
		if err != nil || fi.Size() != 0 || fi.Mode().Perm() != 0o600 {
			t.Errorf("temp file not 0600 and empty before the first byte")
		}
		if live, _ := os.ReadFile(path); !bytes.Equal(live, old.Bytes()) {
			t.Error("live entry touched before the rename")
		}
		return fh.Write(b)
	}
	store.rename = func(from, to string) error {
		if filepath.Dir(from) != dir || to != path {
			t.Errorf("rename %q -> %q is not a same-dir replace of the entry", from, to)
		}
		return os.Rename(from, to)
	}
	err := store.write(context.Background(), dir, next.Bytes())
	if got, _ := os.ReadFile(path); err != nil || !bytes.Equal(got, next.Bytes()) {
		t.Fatal("entry not replaced through the temp file")
	}
	hcOnlyEntryLeft(t, dir)
}

func TestHarnessCredentialsLinuxModeIs0600(t *testing.T) {
	dir := hcSetup(t)
	path := filepath.Join(dir, ".credentials.json")
	h := hcCreds(hcFileStore(), &hcCustody{memCustody: newMemCustody()})
	_, aerr := h.Read(context.Background(), dir)
	hcKind(t, aerr, cascade.KindNotFound, "secrets: no secret named \".credentials.json\" is stored")
	if err := h.WriteDurable(context.Background(), dir, hcEntry("a")); err != nil {
		t.Fatalf("create: %v", err)
	}
	fi, err := os.Stat(path)
	if uid, ok := harnessFileOwner(fi); err != nil || fi.Mode().Perm() != 0o600 || !ok || uid != os.Getuid() {
		t.Fatalf("created entry mode %v, want 0600 owned by the current uid", fi.Mode().Perm())
	}
	wider := hcEntry("wide").Bytes()
	hcWrite(t, path, wider, 0o640)
	const msg = "secrets: the harness credentials file is not a regular 0600 file owned by the current user; refused unchanged"
	_, rerr := h.Read(context.Background(), dir)
	hcKind(t, rerr, cascade.KindPermissionDenied, msg)
	hcKind(t, h.WriteDurable(context.Background(), dir, hcEntry("b")), cascade.KindPermissionDenied, msg)
	fi, _ = os.Stat(path)
	if got, _ := os.ReadFile(path); !bytes.Equal(got, wider) || fi.Mode().Perm() != 0o640 {
		t.Fatal("a refused wider entry was changed")
	}
}

func TestHarnessCredentialsWindowsUnsupported(t *testing.T) {
	cust := &hcCustody{memCustody: newMemCustody()}
	h := hcCreds(unsupportedStore{}, cust)
	_, rerr := h.Read(context.Background(), t.TempDir())
	werr := h.WriteDurable(context.Background(), t.TempDir(), hcEntry("w"))
	const msg = "secrets: harness credential entries are not supported on this platform"
	for _, err := range []error{rerr, werr} {
		if err != errHarnessCredentialsUnsupported {
			t.Fatalf("want the unsupported sentinel, got %v", err)
		}
		hcKind(t, err, cascade.KindUnsupported, msg)
	}
	if len(cust.entries) != 0 {
		t.Fatal("unsupported platform wrote to custody")
	}
	hw, err := NewHarnessCredentials(cust, runtime.NewFixedClock(hcEpoch))
	if err != nil {
		t.Fatalf("constructor refused: %v", err)
	}
	if goruntime.GOOS != "windows" {
		return // the darwin/linux stores would reach the real keychain or config dir
	}
	if _, err := hw.Read(context.Background(), t.TempDir()); err != errHarnessCredentialsUnsupported {
		t.Fatalf("windows constructor: %v", err)
	}
}

func TestHarnessCredentialsEntryNeverFormatsToken(t *testing.T) {
	e := hcEntry("fmt")
	if !bytes.Contains(e.Bytes(), []byte(hcCanary)) {
		t.Fatal("entry does not hold the canary")
	}
	js, err := json.Marshal(e)
	wrapped, _ := json.Marshal(struct{ E Entry }{e})
	for _, out := range []string{fmt.Sprintf("%v %+v %#v", e, e, e), fmt.Sprintf("%v %#v", &e, &e), string(js), string(wrapped)} {
		if err != nil || strings.TrimSpace(out) == "" || strings.Contains(out, hcCanary) || strings.Contains(out, "r-fmt") {
			t.Fatalf("formatted entry is empty or carries token bytes (err %v)", err)
		}
	}
}

func TestKeychainServiceNameLiteralPath(t *testing.T) {
	base := hcSetup(t)
	link := filepath.Join(base, "link")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(link)
	litSvc, realSvc := harnessServiceName(link), harnessServiceName(resolved)
	if litSvc == realSvc {
		t.Fatal("literal and realpath services must differ for a symlinked dir")
	}
	notFound := &runnerError{err: errors.New("exit 44"), stderr: "The specified item could not be found in the keychain."}
	cases := map[string]struct {
		answers map[string]error // service -> result; nil error answers with the entry
		basis   string
		kind    cascade.Kind
	}{
		"literal answers":   {map[string]error{litSvc: nil, realSvc: nil}, "literal", 0},
		"realpath fallback": {map[string]error{litSvc: notFound, realSvc: nil}, "realpath", 0},
		"both absent":       {map[string]error{litSvc: notFound, realSvc: notFound}, "", cascade.KindNotFound},
		"literal unreadable": {map[string]error{litSvc: &runnerError{err: errors.New("exit 51"), stderr: hcCanary}, realSvc: nil},
			"", cascade.KindUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var wrote string
			run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
				svc := args[slices.Index(args, "-s")+1]
				if args[0] == "add-generic-password" {
					wrote = svc
					return nil, nil
				}
				return []byte(`{"accessToken":"` + hcCanary + `"}` + "\n"), tc.answers[svc]
			}
			ks := &keychainStore{run: run, realpath: filepath.EvalSymlinks, basis: map[string][2]string{},
				ident: func(context.Context) (string, string, error) { return "tester", "/fake/login.keychain-db", nil }}
			_, err := ks.read(context.Background(), link)
			if tc.kind != 0 {
				hcKind(t, err, tc.kind, "")
				return
			}
			if err != nil || ks.basis[link][0] != tc.basis {
				t.Fatalf("read err %v, basis %q, want %q", err, ks.basis[link][0], tc.basis)
			}
			if err := ks.write(context.Background(), link, []byte(`{}`)); err != nil || wrote != ks.basis[link][1] {
				t.Fatalf("write went to the wrong service (err %v)", err)
			}
		})
	}
}
