package elevation

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"github.com/acamarata/cascade/pkg/cascade"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func enrollCustody(t *testing.T, k *countingCustodyKey) *memBackend {
	t.Helper()
	b := &memBackend{}
	pub, err := k.PubKeyB64()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewElevationTrustStore(b, fixedClock(time.Unix(1, 0))).Enroll(pub); err != nil {
		t.Fatal(err)
	}
	return b
}
func TestBoundTrustRefusesSwappedRecord(t *testing.T) {
	sel, _ := custodyFixture(t, CustodyPlatform)
	_, other := custodyFixture(t, CustodyPlatform)
	_, _, err := BoundTrust(sel.Select(), enrollCustody(t, other), fixedClock(time.Unix(1, 0)))
	if !cascade.HasKind(err, cascade.KindConflict) || err.Error() != ErrAlreadyEnrolled().Error() {
		t.Fatalf("swapped=%v", err)
	}
}
func TestEnrolledCustodyNeverFallsBackToFile(t *testing.T) {
	if DevkeysBuild() {
		t.Skip("development file signing enabled")
	}
	sel, k := custodyFixture(t, CustodyPlatform)
	b := enrollCustody(t, k)
	file, other := custodyFixture(t, CustodyFile)
	plantCustodyKey(t, sel.DataDir, other)
	sel.Sources[0].Open = func(string) (ElevationKeystore, bool) { return k, false }
	sel.Sources = append(sel.Sources, file.Sources...)
	_, _, err := BoundTrust(sel.Select(), b, fixedClock(time.Unix(1, 0)))
	assertCustodyRefusal(t, err, CustodyFile)
	if k.signs != 0 || other.signs != 0 {
		t.Fatal("signed during fallback refusal")
	}
}
func TestStaleFileEnrollmentNamesReenroll(t *testing.T) {
	sel, _ := custodyFixture(t, CustodyPlatform)
	_, old := custodyFixture(t, CustodyFile)
	plantCustodyKey(t, sel.DataDir, old)
	_, _, err := BoundTrust(sel.Select(), enrollCustody(t, old), fixedClock(time.Unix(1, 0)))
	if !cascade.HasKind(err, cascade.KindConflict) || err.Error() != ErrStaleFileTierEnrollment().Error() || !strings.Contains(err.Error(), "--replace-file-tier") {
		t.Fatalf("stale=%v", err)
	}
}

type orderedCustodyBackend struct {
	*memBackend
	path             string
	keyPresentAtSave bool
}

func (b *orderedCustodyBackend) Save(rec TrustRecord) error {
	_, err := os.Stat(b.path)
	b.keyPresentAtSave = err == nil
	return b.memBackend.Save(rec)
}
func TestReplaceFileTierEnrollment(t *testing.T) {
	for _, mode := range []string{"replace", "unrelated", "save-failure"} {
		t.Run(mode, func(t *testing.T) {
			sel, k := custodyFixture(t, CustodyPlatform)
			_, old := custodyFixture(t, CustodyFile)
			plantCustodyKey(t, sel.DataDir, old)
			b := &orderedCustodyBackend{memBackend: enrollCustody(t, old), path: filepath.Join(sel.DataDir, "elevation.key")}
			before, _ := os.ReadFile(b.path)
			if mode == "unrelated" {
				b.memBackend = enrollCustody(t, k)
			}
			saved := b.rec
			fail := errors.New("save refused")
			if mode == "save-failure" {
				b.saveErr = fail
			}
			fp, err := ReplaceFileTierEnrollment(sel.Select(), b, fixedClock(time.Unix(2, 0)), sel.DataDir)
			if mode != "replace" {
				if err == nil {
					t.Fatal("invalid replacement accepted")
				}
				if mode == "unrelated" && err.Error() != ErrAlreadyEnrolled().Error() {
					t.Fatalf("error=%v", err)
				}
				if mode == "save-failure" && err != fail {
					t.Fatalf("error=%v", err)
				}
				after, _ := os.ReadFile(b.path)
				if !bytes.Equal(before, after) || b.rec != saved {
					t.Fatal("failed replacement changed state")
				}
				return
			}
			if err != nil || fp == "" || !b.keyPresentAtSave {
				t.Fatalf("replace=%s,%v order=%v", fp, err, b.keyPresentAtSave)
			}
			if _, err := os.Stat(b.path); !os.IsNotExist(err) {
				t.Fatalf("stale key remains: %v", err)
			}
			pub, _ := k.PubKeyB64()
			if b.rec.PubKeyB64 != pub || b.rec.FingerprintSHA256 != fp {
				t.Fatal("new record not stored")
			}
		})
	}
}
func TestTrustRecordModeTooOpenRefuses(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	sel, k := custodyFixture(t, CustodyPlatform)
	b := NewFileBackend(sel.DataDir).(fileBackend)
	if err := b.Save(enrollCustody(t, k).rec); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(b.path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.Load(); ok || !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("load=%v,%v", ok, err)
	}
}
func TestTrustRecordForeignOwnerRefuses(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX ownership")
	}
	sel, k := custodyFixture(t, CustodyPlatform)
	b := NewFileBackend(sel.DataDir).(fileBackend)
	if err := b.Save(enrollCustody(t, k).rec); err != nil {
		t.Fatal(err)
	}
	calls := 0
	b.owner = func(os.FileInfo) bool { calls++; return false }
	if _, ok, err := b.Load(); ok || !cascade.HasKind(err, cascade.KindIntegrity) || calls != 1 {
		t.Fatalf("load=%v,%v calls=%d", ok, err, calls)
	}
}
func TestBoundTrustRefusalPaths(t *testing.T) {
	sel, k := custodyFixture(t, CustodyPlatform)
	clock := fixedClock(time.Unix(1, 0))
	for _, backend := range []Backend{nil, &memBackend{}, &memBackend{corrupt: true}, &memBackend{present: true, rec: TrustRecord{PubKeyB64: "invalid"}}} {
		if _, _, err := BoundTrust(sel.Select(), backend, clock); err == nil {
			t.Fatal("invalid trust accepted")
		}
	}
	b := enrollCustody(t, k)
	k.pub = nil
	if _, _, err := BoundTrust(sel.Select(), b, clock); err == nil {
		t.Fatal("missing key accepted")
	}
	k.pub = []byte("short")
	if _, _, err := BoundTrust(sel.Select(), b, clock); err == nil {
		t.Fatal("short key accepted")
	}
}
func TestReplacementRefusalPaths(t *testing.T) {
	sel, k := custodyFixture(t, CustodyPlatform)
	b := enrollCustody(t, k)
	clock := fixedClock(time.Unix(1, 0))
	if _, err := ReplaceFileTierEnrollment(Custody{}, b, clock, sel.DataDir); err == nil {
		t.Fatal("none replaced")
	}
	if _, err := ReplaceFileTierEnrollment(sel.Select(), &memBackend{corrupt: true}, clock, sel.DataDir); err == nil {
		t.Fatal("corrupt record replaced")
	}
	if _, err := ReplaceFileTierEnrollment(sel.Select(), b, clock, sel.DataDir); err == nil {
		t.Fatal("absent file replaced")
	}
	plantCustodyKey(t, sel.DataDir, k)
	k.pub = nil
	if _, err := ReplaceFileTierEnrollment(sel.Select(), b, clock, sel.DataDir); err == nil {
		t.Fatal("missing protected key replaced")
	}
	k.pub = []byte("short")
	if _, err := ReplaceFileTierEnrollment(sel.Select(), b, clock, sel.DataDir); err == nil {
		t.Fatal("invalid protected key replaced")
	}
}
func custodyFiles(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCustodyReportNamesTier(t *testing.T) {
	for _, tier := range []CustodyTier{CustodyPlatform, CustodyPresence, CustodyFile, CustodyNone} {
		sel, k := custodyFixture(t, tier)
		plantCustodyKey(t, sel.DataDir, k)
		b := NewFileBackend(sel.DataDir)
		if err := b.Save(enrollCustody(t, k).rec); err != nil {
			t.Fatal(err)
		}
		before := custodyFiles(t, sel.DataDir)
		status := CustodyReport(sel, b, fixedClock(time.Unix(1, 0)))
		if status.Tier != tier || !status.Enrolled || status.Bound != tier.SatisfiesElevation() {
			t.Fatalf("status=%+v", status)
		}
		if len(before) == 0 || !reflect.DeepEqual(before, custodyFiles(t, sel.DataDir)) || k.signs != 0 {
			t.Fatal("report changed state")
		}
	}
}
func TestCustodyReportUnreadableTrust(t *testing.T) {
	sel, _ := custodyFixture(t, CustodyPlatform)
	for _, b := range []Backend{nil, &memBackend{corrupt: true}} {
		status := CustodyReport(sel, b, fixedClock(time.Unix(1, 0)))
		if status.Enrolled || status.Bound {
			t.Fatalf("status=%+v", status)
		}
	}
}
