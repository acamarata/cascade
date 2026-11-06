package nodes

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func recordUpdateFixture(t *testing.T) (string, *RecordStore, DeviceRecord) {
	t.Helper()
	dir := t.TempDir()
	s := recordUpdateStore(dir)
	rec, err := s.Enroll(testIdentity(t, "record-update"), TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	return dir, s, rec
}

func recordUpdateStore(dir string) *RecordStore {
	return NewRecordStore(NewFileRecordBackend(dir), testkit.NewFrozenClock(time.Unix(100, 0)))
}

func requireRecordError(t *testing.T, err error, kind cascade.Kind, message string) {
	t.Helper()
	got, ok := cascade.KindOf(err)
	if !ok || got != kind || err.Error() != kind.String()+": "+message {
		t.Fatalf("error = %v (kind %v), want %v: %s", err, got, kind, message)
	}
}

func recordBytes(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "nodes", "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestRecordsHelperProcess runs a real store writer in a separate process.
func TestRecordsHelperProcess(t *testing.T) {
	value := os.Getenv("CASCADE_NODES_RECORDS_HELPER")
	if value == "" {
		return
	}
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || parts[0] != "revoke" {
		t.Fatal("invalid helper verb")
	}
	// Split at the last colon so a Windows drive letter remains in dataDir.
	cut := strings.LastIndex(parts[1], ":")
	if cut < 0 {
		t.Fatal("invalid helper arguments")
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := recordUpdateStore(parts[1][:cut]).Revoke(parts[1][cut+1:]); err != nil {
		t.Fatal(err)
	}
}

// heldRecordBackend borrows the lock already owned by the parent test.
type heldRecordBackend struct{ RecordBackend }

func (heldRecordBackend) lockRecords() (func(), error) { return func() {}, nil }

func startRecordRevoke(t *testing.T, dir, nodeID string) <-chan error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecordsHelperProcess$")
	cmd.Env = append(os.Environ(), "CASCADE_NODES_RECORDS_HELPER=revoke:"+dir+":"+nodeID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		_ = cmd.Wait()
		t.Fatalf("helper did not become ready: %s", stderr.String())
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if err != nil {
			err = fmt.Errorf("%v: %s", err, stderr.String())
		}
		done <- err
	}()
	return done
}

// TestRecordUpdateCrossProcessSerializes preserves both processes' edits.
func TestRecordUpdateCrossProcessSerializes(t *testing.T) {
	dir, s, rec := recordUpdateFixture(t)
	unlock, err := s.backend.(recordLocker).lockRecords()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	done := startRecordRevoke(t, dir, rec.NodeID)
	select {
	case err := <-done:
		t.Fatalf("child exited while lock held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	t1 := time.Unix(200, 0).UTC()
	parent := NewRecordStore(heldRecordBackend{s.backend}, s.clock)
	if _, err := parent.update(rec.NodeID, func(r *DeviceRecord) error {
		r.LastSeen = t1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock = nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(rec.NodeID)
	if err != nil || !got.LastSeen.Equal(t1) || !SignatureRevoked(got, rec.PubKeyB64) {
		t.Fatalf("lost concurrent update: %+v, %v", got, err)
	}
}

// TestRecordUpdateFieldScoped preserves revocation after a stale Get.
func TestRecordUpdateFieldScoped(t *testing.T) {
	dir, a, rec := recordUpdateFixture(t)
	snapshot, err := a.Get(rec.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordUpdateStore(dir).Revoke(rec.NodeID); err != nil {
		t.Fatal(err)
	}
	t1 := time.Unix(300, 0).UTC()
	got, err := a.update(snapshot.NodeID, func(r *DeviceRecord) error {
		r.LastSeen = t1
		return nil
	})
	if err != nil || !got.LastSeen.Equal(t1) || !SignatureRevoked(got, rec.PubKeyB64) {
		t.Fatalf("update lost revoked key: %+v, %v", got, err)
	}
	persisted, err := a.Get(rec.NodeID)
	if err != nil || !persisted.LastSeen.Equal(t1) || !SignatureRevoked(persisted, rec.PubKeyB64) {
		t.Fatalf("persisted update lost fields: %+v, %v", persisted, err)
	}
}

// TestRecordLockTimeout bounds contention and checks the public error.
func TestRecordLockTimeout(t *testing.T) {
	dir := t.TempDir()
	unlock, err := NewFileRecordBackend(dir).(recordLocker).lockRecords()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	old := recordLockWait
	recordLockWait = 50 * time.Millisecond
	defer func() { recordLockWait = old }()
	start := time.Now()
	otherUnlock, err := NewFileRecordBackend(dir).(recordLocker).lockRecords()
	if otherUnlock != nil {
		otherUnlock()
	}
	requireRecordError(t, err, cascade.KindUnavailable, "nodes: device record store is locked by another process")
	if elapsed := time.Since(start); elapsed < recordLockWait || elapsed >= time.Second {
		t.Fatalf("lock timeout took %v", elapsed)
	}
}

// TestRecordUpdateUnknownNode refuses a missing row without writing.
func TestRecordUpdateUnknownNode(t *testing.T) {
	dir, s, _ := recordUpdateFixture(t)
	before := recordBytes(t, dir)
	_, err := s.update("ghost", func(*DeviceRecord) error {
		t.Fatal("callback invoked for unknown node")
		return nil
	})
	requireRecordError(t, err, cascade.KindNotFound, `nodes: no device record for node "ghost"`)
	if !bytes.Equal(before, recordBytes(t, dir)) {
		t.Fatal("unknown-node update changed devices.json")
	}
}

// TestRecordUpdateFnErrorNoWrite returns callback errors unchanged.
func TestRecordUpdateFnErrorNoWrite(t *testing.T) {
	dir, s, rec := recordUpdateFixture(t)
	before := recordBytes(t, dir)
	want := cascade.New(cascade.KindConflict, "record update refused")
	_, err := s.update(rec.NodeID, func(r *DeviceRecord) error {
		r.Drained = true
		return want
	})
	if err != want {
		t.Fatalf("callback error changed: %v", err)
	}
	requireRecordError(t, err, cascade.KindConflict, "record update refused")
	if !bytes.Equal(before, recordBytes(t, dir)) {
		t.Fatal("failed callback changed devices.json")
	}
}

// TestRemoveThenUpdateNoResurrect keeps stale callers from restoring a row.
func TestRemoveThenUpdateNoResurrect(t *testing.T) {
	dir, a, rec := recordUpdateFixture(t)
	if _, err := a.Get(rec.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := recordUpdateStore(dir).Remove(rec.NodeID); err != nil {
		t.Fatal(err)
	}
	before := recordBytes(t, dir)
	_, err := a.update(rec.NodeID, func(r *DeviceRecord) error { r.Drained = true; return nil })
	requireRecordError(t, err, cascade.KindNotFound, fmt.Sprintf("nodes: no device record for node %q", rec.NodeID))
	rows, err := a.List()
	if err != nil || len(rows) != 0 || !bytes.Equal(before, recordBytes(t, dir)) {
		t.Fatalf("removed node resurrected: %+v, %v", rows, err)
	}
}
