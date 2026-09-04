package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newStore returns a FileStore rooted in a fresh temp directory with a
// frozen clock. Every test in this file writes only under t.TempDir()
// (Art.7.1); nothing here touches HOME or the system temp directory
// directly.
func newStore(t *testing.T) (*FileStore, *testClockRef, string) {
	t.Helper()
	base := t.TempDir()
	clk := newTestClock()
	return NewFileStore(base, clk), &testClockRef{clk}, base
}

// testClockRef gives the tests a name for the frozen clock they advance.
type testClockRef struct {
	c interface{ Advance(time.Duration) time.Time }
}

func (r *testClockRef) advance(d time.Duration) { r.c.Advance(d) }

func TestWriteReadRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _, base := newStore(t)
	want := validEntry()
	want.ExpiresAt = ptrTime(fixedNow.Add(24 * time.Hour))
	want.CommitSHA = "abc123"
	want.Supersedes = "project/older"

	if err := s.Write(ctx, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := s.Read(ctx, want.Kind, want.Name)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want.Provenance.CreatedAt = fixedNow
	want.Provenance.UpdatedAt = fixedNow
	want.Provenance.ContentHash = HashBody(want.Body)
	assertEntryEqual(t, got, want.canonical())

	// The layout is part of the contract: a later projection walks this
	// tree by path, so the path is asserted rather than assumed.
	if _, err := os.Stat(filepath.Join(base, "project", "a-record.md")); err != nil {
		t.Errorf("record is not at {base}/{kind}/{name}.md: %v", err)
	}
}

// TestWriteIsIdempotent proves a repeated identical write neither rewrites
// the file nor moves its timestamps, which is what lets a caller re-assert
// a record cheaply.
func TestWriteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, clk, base := newStore(t)
	e := validEntry()
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	path := filepath.Join(base, "project", "a-record.md")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	firstBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	clk.advance(time.Hour)
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("repeat Write: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	secondBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an idempotent write touched the file's mtime")
	}
	if string(firstBytes) != string(secondBytes) {
		t.Error("an idempotent write changed the file's bytes")
	}
}

// TestWriteBodyChangeBumpsHashAndUpdatedAt is the other half of
// idempotency: a real change must be recorded.
func TestWriteBodyChangeBumpsHashAndUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newStore(t)
	e := validEntry()
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	clk.advance(90 * time.Minute)
	e.Body = "a different body\n"
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	got, err := s.Read(ctx, e.Kind, e.Name)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Provenance.ContentHash != HashBody(e.Body) {
		t.Error("content hash was not updated for the new body")
	}
	if !got.Provenance.CreatedAt.Equal(fixedNow) {
		t.Errorf("created_at moved on rewrite: %s", got.Provenance.CreatedAt)
	}
	if !got.Provenance.UpdatedAt.Equal(fixedNow.Add(90 * time.Minute)) {
		t.Errorf("updated_at = %s, want the advanced clock instant", got.Provenance.UpdatedAt)
	}
}

// TestWriteMetadataChangeIsPersisted guards the trap in a hash-only
// idempotency check: the body is unchanged, so a store comparing body
// hashes alone would call this a no-op and silently drop the new
// description and confidence.
func TestWriteMetadataChangeIsPersisted(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newStore(t)
	e := validEntry()
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	clk.advance(time.Minute)
	e.Description = "a corrected description"
	e.Confidence = 0.25
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("metadata Write: %v", err)
	}
	got, err := s.Read(ctx, e.Kind, e.Name)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Description != "a corrected description" || got.Confidence != 0.25 {
		t.Fatalf("metadata change was dropped: description=%q confidence=%v",
			got.Description, got.Confidence)
	}
}

func TestReadMissingAndTombstoned(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)

	_, err := s.Read(ctx, KindProject, "never-written")
	assertNotFound(t, err)

	e := validEntry()
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Delete(ctx, e.Kind, e.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = s.Read(ctx, e.Kind, e.Name)
	assertNotFound(t, err)
}

func assertNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrNoSuchEntry) {
		t.Fatalf("error = %v, want ErrNoSuchEntry", err)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("error %v is not a KindNotFound taxonomy error", err)
	}
}

// TestDeleteWritesTombstoneBesideTheRecord pins the on-disk deletion
// marker, which a later projection detects without a second source to diff
// against.
func TestDeleteWritesTombstoneBesideTheRecord(t *testing.T) {
	ctx := context.Background()
	s, _, base := newStore(t)
	e := validEntry()
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Delete(ctx, e.Kind, e.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "project", "a-record.md.tombstone")); err != nil {
		t.Errorf("no tombstone at {name}.md.tombstone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "project", "a-record.md")); !os.IsNotExist(err) {
		t.Errorf("record file survived deletion: %v", err)
	}
	assertNotFound(t, s.Delete(ctx, e.Kind, e.Name))
	assertNotFound(t, s.Delete(ctx, KindUser, "never-existed"))
}

// TestTombstoneWinsOverASurvivingRecord is the interrupted-delete case.
// The tombstone is written first, so a crash between the two steps leaves
// both files; the deletion must still be in force.
func TestTombstoneWinsOverASurvivingRecord(t *testing.T) {
	ctx := context.Background()
	s, _, base := newStore(t)
	e := validEntry()
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	tomb := filepath.Join(base, "project", "a-record.md.tombstone")
	if err := os.WriteFile(tomb, nil, 0o600); err != nil {
		t.Fatalf("simulating an interrupted delete: %v", err)
	}
	_, readErr := s.Read(ctx, e.Kind, e.Name)
	assertNotFound(t, readErr)
	ok, err := s.Exists(ctx, e.Kind, e.Name)
	if err != nil || ok {
		t.Errorf("Exists = %v, %v; want false, nil", ok, err)
	}
	names, err := s.List(ctx, e.Kind)
	if err != nil || len(names) != 0 {
		t.Errorf("List = %v, %v; want empty", names, err)
	}
}

// TestWriteResurrectsATombstonedName proves a deleted name is reusable,
// rather than being permanently unreachable.
func TestWriteResurrectsATombstonedName(t *testing.T) {
	ctx := context.Background()
	s, _, base := newStore(t)
	e := validEntry()
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Delete(ctx, e.Kind, e.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("resurrecting Write: %v", err)
	}
	if _, err := s.Read(ctx, e.Kind, e.Name); err != nil {
		t.Fatalf("Read after resurrection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "project", "a-record.md.tombstone")); !os.IsNotExist(err) {
		t.Errorf("tombstone survived the rewrite: %v", err)
	}
}

func TestUpdateRequiresAnExistingRecord(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newStore(t)
	e := validEntry()
	assertNotFound(t, s.Update(ctx, e))

	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	clk.advance(time.Hour)
	e.Body = "updated body\n"
	if err := s.Update(ctx, e); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Read(ctx, e.Kind, e.Name)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Body != "updated body\n" {
		t.Errorf("body = %q after Update", got.Body)
	}
	if err := s.Delete(ctx, e.Kind, e.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	assertNotFound(t, s.Update(ctx, e))
}
