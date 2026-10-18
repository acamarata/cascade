// Purpose: edge and error branches of custody selection, the source
//
//	registry, the private-directory rule, stale-file replacement and the
//	trust-record file backend.
//
// SPORT: internal/elevation custody edge tests/ADDED (P1-SHIP-15).
package elevation

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCustodyRankOrdersTiersAndTreatsUnknownAsNone(t *testing.T) {
	for _, tc := range []struct {
		tier CustodyTier
		want int
	}{
		{CustodyPlatform, 3},
		{CustodyPresence, 2},
		{CustodyFile, 1},
		{CustodyNone, 0},
		{CustodyTier("bogus"), 0},
	} {
		if got := custodyRank(tc.tier); got != tc.want {
			t.Errorf("custodyRank(%q) = %d, want %d", tc.tier, got, tc.want)
		}
	}
}

func sourceNames(sources []CustodySource) []string {
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Name)
	}
	return names
}

func TestSourcesForOSListsRegistryPerPlatform(t *testing.T) {
	old := presenceSource
	t.Cleanup(func() { presenceSource = old })
	presenceSource = &CustodySource{Tier: CustodyPresence, Name: "presence"}
	plat := func() ElevationKeystore { return &fakeKeystore{available: true} }
	for _, tc := range []struct {
		goos string
		want []string
	}{
		{"windows", nil},
		{"darwin", []string{"platform", "file"}},
		{"linux", []string{"linux-pam", "presence", "file"}},
		{"freebsd", []string{"file"}},
	} {
		got := sourceNames(sourcesForOS(tc.goos, plat))
		if len(got) != len(tc.want) {
			t.Fatalf("%s sources = %v, want %v", tc.goos, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s sources = %v, want %v", tc.goos, got, tc.want)
			}
		}
	}
	presenceSource = nil
	if got := sourceNames(sourcesForOS("linux", plat)); len(got) != 2 || got[0] != "linux-pam" || got[1] != "file" {
		t.Fatalf("linux without a presence source = %v", got)
	}
}

func TestSourcesForOSOpenRefusesAnUnavailablePlatformKeystore(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		for _, tc := range []struct {
			name string
			ks   ElevationKeystore
			want bool
		}{
			{"available", &fakeKeystore{available: true}, true},
			{"unavailable", &fakeKeystore{available: false}, false},
			{"absent", nil, false},
		} {
			src := sourcesForOS(goos, func() ElevationKeystore { return tc.ks })[0]
			ks, ok := src.Open(t.TempDir())
			if ok != tc.want {
				t.Errorf("%s/%s: usable = %v, want %v", goos, tc.name, ok, tc.want)
			}
			if tc.ks == nil && ks != nil {
				t.Errorf("%s/%s: returned a keystore for an absent platform one", goos, tc.name)
			}
		}
	}
}

func TestSourcesForOSFileSourceNeedsADirectory(t *testing.T) {
	file := sourcesForOS("freebsd", nil)[0]
	if _, ok := file.Open(""); ok {
		t.Fatal("file source opened with no data directory")
	}
	sel, k := custodyFixture(t, CustodyFile)
	plantCustodyKey(t, sel.DataDir, k)
	if _, ok := file.Open(sel.DataDir); !ok {
		t.Fatal("file source refused a directory holding a key")
	}
}

func TestEnrollPresenceRefusesWhereNoPresenceTierExists(t *testing.T) {
	old := presenceEnroll
	t.Cleanup(func() { presenceEnroll = old })
	presenceEnroll = nil
	if _, err := EnrollPresence(t.TempDir(), &memBackend{}, fixedClock(time.Unix(1, 0)), nil); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("no presence tier: err = %v", err)
	}
	dir := t.TempDir()
	want := errors.New("presence enrol ran")
	presenceEnroll = func(got string, _ Backend, _ Clock, _ PresenceAuditor) (string, error) {
		if got != dir {
			t.Errorf("data dir = %q, want %q", got, dir)
		}
		return "fp-1", want
	}
	if fp, err := EnrollPresence(dir, &memBackend{}, fixedClock(time.Unix(1, 0)), nil); fp != "fp-1" || err != want {
		t.Fatalf("delegated enrol = %q, %v", fp, err)
	}
}

func TestCheckPrivateBaseRefusesMissingAndNonDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := checkPrivateBase(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing base: err = %v", err)
	}
	file := filepath.Join(dir, "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateBase(file); err == nil || err.Error() != "temp directory is not a directory" {
		t.Fatalf("file as base: err = %v", err)
	}
}

type removingBackend struct {
	*memBackend
	keyPath string
}

// Save removes the stale key the way a racing process could, after the
// record is stored.
func (b removingBackend) Save(rec TrustRecord) error {
	if err := b.memBackend.Save(rec); err != nil {
		return err
	}
	return os.Remove(b.keyPath)
}

func TestReplaceFileTierEnrollmentReportsAStaleKeyThatCannotBeRemoved(t *testing.T) {
	sel, k := custodyFixture(t, CustodyPlatform)
	_, old := custodyFixture(t, CustodyFile)
	plantCustodyKey(t, sel.DataDir, old)
	b := removingBackend{memBackend: enrollCustody(t, old), keyPath: filepath.Join(sel.DataDir, "elevation.key")}
	fp, err := ReplaceFileTierEnrollment(sel.Select(), b, fixedClock(time.Unix(2, 0)), sel.DataDir)
	if fp != "" || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("replace = %q, %v", fp, err)
	}
	pub, _ := k.PubKeyB64()
	if b.rec.PubKeyB64 != pub {
		t.Fatal("the new record was not stored before the removal failed")
	}
}

func TestFileBackendLoadRefusesAnUnreadableRecord(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX directory reads")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	if err := os.Mkdir(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	if rec, ok, err := (fileBackend{path: path}).Load(); err == nil || ok || rec != (TrustRecord{}) {
		t.Fatalf("directory as record: %v, %v, %v", rec, ok, err)
	}
}

type fakeFileInfo struct {
	os.FileInfo
	sys any
}

func (f fakeFileInfo) Sys() any { return f.sys }

func TestTrustOwnerNeedsARealOwnedStat(t *testing.T) {
	type stat struct{ Uid uint32 } //nolint:revive // mirrors syscall.Stat_t: trustOwner reads the field by the name "Uid" through reflection
	for _, tc := range []struct {
		name string
		sys  any
		want bool
	}{
		{"no sys", nil, false},
		{"not a pointer", stat{Uid: uint32(os.Geteuid())}, false},
		{"nil pointer", (*stat)(nil), false},
		{"no uid field", &struct{ Gid uint32 }{}, false},
		{"other owner", &stat{Uid: uint32(os.Geteuid()) + 1}, false},
		{"current owner", &stat{Uid: uint32(os.Geteuid())}, true},
	} {
		if got := trustOwner(fakeFileInfo{sys: tc.sys}); got != tc.want {
			t.Errorf("%s: trustOwner = %v, want %v", tc.name, got, tc.want)
		}
	}
}
