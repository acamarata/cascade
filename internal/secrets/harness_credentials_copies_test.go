package secrets

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// hcKeychain is a fake security tool: items maps a service to its stored
// text; every read and write records the service it named.
type hcKeychain struct {
	items     map[string]string
	readErr   map[string]error
	writeFail string
	reads     []string
	writes    []string
}

var hcNotFound = &runnerError{err: errors.New("exit 44"), stderr: "The specified item could not be found in the keychain."}

func (f *hcKeychain) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	svc := args[slices.Index(args, "-s")+1]
	if args[0] == "add-generic-password" {
		f.writes = append(f.writes, svc)
		if svc == f.writeFail {
			return nil, &runnerError{err: errors.New("exit 1"), stderr: "write refused"}
		}
		f.items[svc] = args[slices.Index(args, "-X")+1]
		return nil, nil
	}
	f.reads = append(f.reads, svc)
	if err := f.readErr[svc]; err != nil {
		return nil, err
	}
	v, ok := f.items[svc]
	if !ok {
		return nil, hcNotFound
	}
	return []byte(v + "\n"), nil
}

// hcCountCustody records every slot name Set is called with.
type hcCountCustody struct {
	*memCustody
	sets []string
}

func (c *hcCountCustody) Set(ctx context.Context, n string, v []byte) error {
	c.sets = append(c.sets, n)
	return c.memCustody.Set(ctx, n, v)
}

// hcLinked returns a resolved home (HOME points there), a realDir dir and a
// symlink to it, with their client services.
func hcLinked(t *testing.T) (home, realDir, link string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(hcSetup(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	realDir, link = filepath.Join(home, "realDir"), filepath.Join(home, "link")
	if err := errors.Join(os.Mkdir(realDir, 0o700), os.Symlink(realDir, link)); err != nil {
		t.Fatal(err)
	}
	return home, realDir, link
}

func hcKeychainCreds(f *hcKeychain, c Custody) *harnessCredentials {
	ks := &keychainStore{run: f.run, realpath: filepath.EvalSymlinks, basis: map[string][2]string{},
		ident: func(context.Context) (string, string, error) { return "tester", "/fake/login.keychain-db", nil }}
	return hcCreds(ks, c)
}

func hcLabels(refs []CopyRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Label)
	}
	return out
}

func hcNamesLabel(t *testing.T, err error, label string) {
	t.Helper()
	if err == nil || cascade.HasKind(err, cascade.KindNotFound) || !strings.Contains(err.Error(), fmt.Sprintf("%q", label)) {
		t.Fatalf("error %v does not name copy %q or reads as absent", err, label)
	}
	if strings.Contains(err.Error(), hcCanary) {
		t.Fatal("error text carries entry bytes")
	}
}

func TestDaemonCopiesNamesEveryReadCopy(t *testing.T) {
	home, realDir, link := hcLinked(t)
	resolve := filepath.EvalSymlinks
	cases := map[string]struct {
		goos, dir, def string
		labels         []string
		services       []string
	}{
		"darwin symlink": {"darwin", link, home, []string{"client", "realpath"},
			[]string{harnessServiceName(link), harnessServiceName(realDir)}},
		"darwin plain": {"darwin", realDir, home, []string{"client"}, []string{harnessServiceName(realDir)}},
		"darwin default": {"darwin", realDir, realDir + "/", []string{"client", "default"},
			[]string{harnessServiceName(realDir), strings.TrimSuffix(vendorKeychainServicePrefix, "-")}},
		"linux": {"linux", link, home, []string{"client"}, []string{""}},
	}
	for name, tc := range cases {
		refs, err := daemonCopiesFor(tc.goos, tc.dir, resolve, tc.def)
		if err != nil || !slices.Equal(hcLabels(refs), tc.labels) {
			t.Fatalf("%s: labels %v (err %v), want %v", name, hcLabels(refs), err, tc.labels)
		}
		for i, r := range refs {
			if r.service != tc.services[i] {
				t.Fatalf("%s: copy %s names the wrong service", name, r.Label)
			}
			if out := fmt.Sprintf("%v %+v %#v %s %v", r, r, r, r, refs); strings.Contains(out, home) {
				t.Fatalf("%s: a CopyRef format carries a path", name)
			}
		}
	}
	if refs, _ := daemonCopiesFor("linux", link, nil, ""); refs[0].path != filepath.Join(link, ".credentials.json") {
		t.Fatal("linux copy is not <dir>/.credentials.json")
	}
	failing := func(string) (string, error) { return "", errors.New("lstat failed") }
	if _, err := daemonCopiesFor("darwin", link, failing, home); err == nil {
		t.Fatal("a resolve error was dropped")
	}
	if _, err := daemonCopiesFor("windows", link, resolve, home); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("windows: %v, want KindUnsupported", err)
	}
	if _, err := DaemonCopies("relative/dir", resolve, home); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("relative dir: %v, want KindInvalidInput", err)
	}
}

func TestReadCopiesReadsOnlyDaemonCopiesAndClassifiesErrors(t *testing.T) {
	home, realDir, link := hcLinked(t)
	live := hcEntry("live").Bytes()
	f := &hcKeychain{readErr: map[string]error{}, items: map[string]string{
		harnessServiceName(link):                             hex.EncodeToString(live),
		harnessServiceName(filepath.Join(home, "x")):         hex.EncodeToString(hcEntry("other").Bytes()),
		strings.TrimSuffix(vendorKeychainServicePrefix, "-"): hex.EncodeToString(hcEntry("default").Bytes()),
		"foreign-service":                                    hex.EncodeToString(hcEntry("foreign").Bytes()),
	}}
	h := hcKeychainCreds(f, newMemCustody())
	got, err := h.ReadCopies(context.Background(), link)
	if err != nil || len(got) != 2 || got[0].Label != "client" || got[0].Absent || string(got[0].Entry.Bytes()) != string(live) {
		t.Fatalf("client copy not read (err %v)", err)
	}
	if got[1].Label != "realpath" || !got[1].Absent {
		t.Fatal("a not-found realpath copy is not Absent")
	}
	if want := []string{harnessServiceName(link), harnessServiceName(realDir)}; !slices.Equal(f.reads, want) {
		t.Fatalf("read %d services, want exactly the DaemonCopies services, client first", len(f.reads))
	}
	f.readErr[harnessServiceName(realDir)] = &runnerError{err: errors.New("exit 36"),
		stderr: "security: SecKeychainSearchCopyNext: User interaction is not allowed. " + hcCanary}
	got, err = h.ReadCopies(context.Background(), link)
	hcNamesLabel(t, err, "realpath")
	if got != nil {
		t.Fatal("a locked copy returned entries")
	}
	delete(f.readErr, harnessServiceName(realDir))
	f.items[harnessServiceName(link)] = "not-json"
	_, err = h.ReadCopies(context.Background(), link)
	hcNamesLabel(t, err, "client")
	hcWrite(t, filepath.Join(realDir, ".credentials.json"), live, 0o644)
	_, err = hcCreds(hcFileStore(), newMemCustody()).ReadCopies(context.Background(), realDir)
	hcNamesLabel(t, err, "client")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("file permission error: %v, want KindPermissionDenied", err)
	}
}

func TestWriteDurableWritesEveryCopy(t *testing.T) {
	_, realDir, link := hcLinked(t)
	old, next := hcEntry("old").Bytes(), hcEntry("new")
	f := &hcKeychain{readErr: map[string]error{}, items: map[string]string{
		harnessServiceName(link):    hex.EncodeToString([]byte("{}")),
		harnessServiceName(realDir): hex.EncodeToString(old),
	}}
	cust := &hcCountCustody{memCustody: newMemCustody()}
	h := hcKeychainCreds(f, cust)
	if err := h.WriteDurable(context.Background(), link, next); err != nil {
		t.Fatalf("write: %v", err)
	}
	staged, _ := slotNames(link)
	var stagedSets, rollbacks []string
	for _, n := range cust.sets {
		if n == staged {
			stagedSets = append(stagedSets, n)
		} else if strings.HasSuffix(n, ".rollback") {
			rollbacks = append(rollbacks, n)
		}
	}
	realSlot := copyRollbackSlot(link, "realpath")
	if len(stagedSets) != 1 || !slices.Equal(rollbacks, []string{realSlot}) || len(f.writes) != 2 {
		t.Fatalf("staged sets %d, rollback slots %d, copy writes %d; want 1, 1 (realpath), 2",
			len(stagedSets), len(rollbacks), len(f.writes))
	}
	if slot := cust.entries[realSlot]; len(slot) < 8 || string(slot[8:]) != string(old) {
		t.Fatal("realpath rollback slot does not hold its prior")
	}
	if _, ok := cust.entries[staged]; ok {
		t.Fatal("staging slot left behind")
	}
	copies, err := h.ReadCopies(context.Background(), link)
	for _, c := range copies {
		if c.Absent || string(c.Entry.Bytes()) != string(next.Bytes()) {
			t.Fatalf("copy %s does not read back the new entry", c.Label)
		}
	}
	if err != nil || len(copies) != 2 {
		t.Fatalf("read back %d copies (err %v)", len(copies), err)
	}
	f.writes = nil
	if err := h.WriteDurable(context.Background(), link, next); err != nil || len(f.writes) != 0 {
		t.Fatalf("second write: %d copy writes (err %v), want 0", len(f.writes), err)
	}
}

func TestWriteDurableCopyFailureSurfacedEntryByteIdentical(t *testing.T) {
	_, realDir, link := hcLinked(t)
	clientSvc, realSvc := harnessServiceName(link), harnessServiceName(realDir)
	priorA, priorB := hex.EncodeToString(hcEntry("a").Bytes()), hex.EncodeToString(hcEntry("b").Bytes())
	f := &hcKeychain{readErr: map[string]error{}, writeFail: realSvc,
		items: map[string]string{clientSvc: priorA, realSvc: priorB}}
	cust := &hcCountCustody{memCustody: newMemCustody()}
	err := hcKeychainCreds(f, cust).WriteDurable(context.Background(), link, hcEntry("new"))
	hcNamesLabel(t, err, "realpath")
	if !slices.Contains(f.writes, clientSvc) {
		t.Fatal("the first copy was never written, so its restore is untested")
	}
	if f.items[clientSvc] != priorA || f.items[realSvc] != priorB {
		t.Fatal("a copy is not byte-identical to its prior after a failed fan-out")
	}
	staged, _ := slotNames(link)
	if _, ok := cust.entries[staged]; ok {
		t.Fatal("staging slot left behind after a failed write")
	}
}
