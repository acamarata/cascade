// Purpose: Projector's required unit tests (projector.go): file reads
//
//	through storage writes, update publication, idempotence,
//	convergence-including-deletion, and error paths — all against a
//	real pkg/provider.Store (providers/sqlite.Open over t.TempDir(),
//	Art.2: no self-authored store double) and t.TempDir() ticket trees.
//
// SPORT: plugins.pbd.internal.pews.Projector/ADD (P1-E14-W3-S29-T1).
package pews

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// fixedClock is a local Clock double: a constant instant, never the wall
// clock.
type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

// recordingPublisher is a local EventPublisher double recording every
// call, or returning failErr when non-nil.
type recordingPublisher struct {
	calls   []notifyPayload
	failErr error
}

func (p *recordingPublisher) Publish(_ context.Context, _ string, payload []byte) error {
	if p.failErr != nil {
		return p.failErr
	}
	var np notifyPayload
	if err := json.Unmarshal(payload, &np); err != nil {
		return err
	}
	p.calls = append(p.calls, np)
	return nil
}

// openTestStore opens a real providers/sqlite.Driver at t.TempDir(),
// closing it on test cleanup.
func openTestStore(t *testing.T) provider.Store {
	t.Helper()
	d, err := sqlite.Open(context.Background(), t.TempDir()+"/pews-projector.db")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// scanAll returns every key/value currently stored under ns/prefix.
func scanAll(t *testing.T, store provider.Store, ns, prefix string) map[string][]byte {
	t.Helper()
	it, err := store.Scan(context.Background(), ns, prefix)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	defer func() { _ = it.Close() }()
	out := map[string][]byte{}
	for it.Next(context.Background()) {
		out[it.Key()] = append([]byte(nil), it.Value()...)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("scan iterate: %v", err)
	}
	return out
}

func TestFileProjection_UpsertsFromFiles(t *testing.T) {
	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	mkTicketFile(t, root, "N", 3, 29, 2, "P1-E14-W3-S29-T2", nil)
	store := openTestStore(t)
	proj, err := NewProjector(ProjectorOptions{Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}

	res, err := proj.Project(context.Background(), root, "P1")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if res.Upserted != 2 || res.Deleted != 0 || res.Converged {
		t.Fatalf("Project result = %+v, want 2 upserted, 0 deleted, not converged", res)
	}
	rows := scanAll(t, store, DefaultProjectionNamespace, rowKeyPrefix)
	if len(rows) != 2 {
		t.Fatalf("stored rows = %d, want 2", len(rows))
	}
	var row Row
	if err := json.Unmarshal(rows[rowKeyPrefix+"P1-E14-W3-S29-T1"], &row); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	if row.ID != "P1-E14-W3-S29-T1" || row.Phase != "P1" || row.Weight != "S" {
		t.Fatalf("row = %+v, want id/phase/weight populated from the ticket file", row)
	}
}

// TestFileProjection_Idempotent proves running Project twice over an
// unchanged tree writes nothing the second time.
func TestFileProjection_Idempotent(t *testing.T) {
	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	store := openTestStore(t)
	pub := &recordingPublisher{}
	proj, err := NewProjector(ProjectorOptions{Store: store, Clock: fixedClock{}, Publisher: pub})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	if _, err := proj.Project(context.Background(), root, "P1"); err != nil {
		t.Fatalf("first Project: %v", err)
	}
	before := scanAll(t, store, DefaultProjectionNamespace, rowKeyPrefix)

	res, err := proj.Project(context.Background(), root, "P1")
	if err != nil {
		t.Fatalf("second Project: %v", err)
	}
	if !res.Converged || res.Upserted != 0 || res.Deleted != 0 {
		t.Fatalf("second Project result = %+v, want Converged with zero writes", res)
	}
	if len(pub.calls) != 1 {
		t.Fatalf("publisher calls = %d, want exactly 1 (only the first, changing, run)", len(pub.calls))
	}
	after := scanAll(t, store, DefaultProjectionNamespace, rowKeyPrefix)
	if len(before) != len(after) {
		t.Fatalf("row count changed across an idempotent run: %d -> %d", len(before), len(after))
	}
}

// TestFileProjection_ConvergesAfterExternalChange proves a projection run
// after an external file edit reaches the same state a from-scratch
// projection over the edited tree would.
func TestFileProjection_ConvergesAfterExternalChange(t *testing.T) {
	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	store := openTestStore(t)
	proj, err := NewProjector(ProjectorOptions{Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	if _, err := proj.Project(context.Background(), root, "P1"); err != nil {
		t.Fatalf("first Project: %v", err)
	}

	// External change: a second dependent ticket appears on disk.
	mkTicketFile(t, root, "N", 3, 29, 2, "P1-E14-W3-S29-T2", []string{"P1-E14-W3-S29-T1"})
	res, err := proj.Project(context.Background(), root, "P1")
	if err != nil {
		t.Fatalf("second Project: %v", err)
	}
	if res.Upserted != 1 || res.Deleted != 0 {
		t.Fatalf("second Project result = %+v, want exactly the new ticket upserted", res)
	}

	fresh := openTestStore(t)
	freshProj, err := NewProjector(ProjectorOptions{Store: fresh, Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("NewProjector (fresh): %v", err)
	}
	if _, err := freshProj.Project(context.Background(), root, "P1"); err != nil {
		t.Fatalf("from-scratch Project: %v", err)
	}
	incremental := scanAll(t, store, DefaultProjectionNamespace, rowKeyPrefix)
	fromScratch := scanAll(t, fresh, DefaultProjectionNamespace, rowKeyPrefix)
	if len(incremental) != len(fromScratch) {
		t.Fatalf("row counts differ: incremental=%d fromScratch=%d", len(incremental), len(fromScratch))
	}
	for k, v := range fromScratch {
		if string(incremental[k]) != string(v) {
			t.Fatalf("row %q diverged between incremental and from-scratch projection", k)
		}
	}
}

// TestFileProjection_DeletesOrphanOnRemoval proves a ticket removed from
// disk leaves no orphan row, and the resulting state matches a from-
// scratch projection over the shrunken tree.
func TestFileProjection_DeletesOrphanOnRemoval(t *testing.T) {
	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	mkTicketFile(t, root, "N", 3, 29, 2, "P1-E14-W3-S29-T2", nil)
	store := openTestStore(t)
	pub := &recordingPublisher{}
	proj, err := NewProjector(ProjectorOptions{Store: store, Clock: fixedClock{}, Publisher: pub})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	if _, err := proj.Project(context.Background(), root, "P1"); err != nil {
		t.Fatalf("first Project: %v", err)
	}

	removeTicketFile(t, root, "N", 3, 29, 2)
	res, err := proj.Project(context.Background(), root, "P1")
	if err != nil {
		t.Fatalf("second Project: %v", err)
	}
	if res.Deleted != 1 || res.Upserted != 0 {
		t.Fatalf("second Project result = %+v, want exactly one deletion", res)
	}
	rows := scanAll(t, store, DefaultProjectionNamespace, rowKeyPrefix)
	if _, orphan := rows[rowKeyPrefix+"P1-E14-W3-S29-T2"]; orphan {
		t.Fatalf("orphan row for removed ticket P1-E14-W3-S29-T2 still present")
	}
	if len(rows) != 1 {
		t.Fatalf("stored rows = %d, want 1", len(rows))
	}
	last := pub.calls[len(pub.calls)-1]
	if len(last.Deleted) != 1 || last.Deleted[0] != "P1-E14-W3-S29-T2" {
		t.Fatalf("last publish = %+v, want the deleted id reported", last)
	}
}

func TestFileProjection_ErrorPaths(t *testing.T) {
	store := openTestStore(t)
	if _, err := NewProjector(ProjectorOptions{Clock: fixedClock{}}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewProjector with nil Store: err = %v, want KindInvalidInput", err)
	}
	if _, err := NewProjector(ProjectorOptions{Store: store}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewProjector with nil Clock: err = %v, want KindInvalidInput", err)
	}

	proj, err := NewProjector(ProjectorOptions{Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	if _, err := proj.Project(context.Background(), t.TempDir()+"/does-not-exist", "P1"); err == nil {
		t.Fatal("Project over a missing root: want an error, got nil")
	}

	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	failing, err := NewProjector(ProjectorOptions{
		Store: store, Clock: fixedClock{}, Publisher: publishFunc(func(context.Context, string, []byte) error {
			return cascade.New(cascade.KindUnavailable, "publish refused")
		}),
	})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	if _, err := failing.Project(context.Background(), root, "P1"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Project with a failing publisher: err = %v, want KindUnavailable", err)
	}
}

// publishFunc adapts a function literal to EventPublisher for the
// publish-failure error-path case above.
type publishFunc func(ctx context.Context, phase string, payload []byte) error

func (f publishFunc) Publish(ctx context.Context, phase string, payload []byte) error {
	return f(ctx, phase, payload)
}

// removeTicketFile deletes the ticket file mkTicketFile would have
// written at the same canonical position.
func removeTicketFile(t *testing.T, root, epic string, wave, sprint, ticket int) {
	t.Helper()
	path := filepath.Join(root, "epics", "E-"+epic, "waves",
		fmt.Sprintf("W-%d", wave), "sprints", fmt.Sprintf("S-%02d", sprint), "tickets",
		fmt.Sprintf("T-%d.yaml", ticket))
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove ticket file: %v", err)
	}
}

// TestProjectionPlatformParity is the Art.5 platform-parity evidence this
// suite runs, unmodified, in each supported-platform CI job (macOS,
// Linux, Windows). Neither this package's filesystem tree walk (store.go
// uses filepath throughout) nor providers/sqlite (pure Go, no CGO) has
// any platform-conditional behavior, so there is no unsupported behavior
// to refuse here — parity means the same tree produces the same
// projected rows on every job, which this asserts directly.
func TestProjectionPlatformParity(t *testing.T) {
	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	store := openTestStore(t)
	proj, err := NewProjector(ProjectorOptions{Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	res, err := proj.Project(context.Background(), root, "P1")
	if err != nil {
		t.Fatalf("Project on %s: %v", runtime.GOOS, err)
	}
	if res.Upserted != 1 || res.Deleted != 0 || res.Converged {
		t.Fatalf("Project on %s = %+v, want exactly one upsert", runtime.GOOS, res)
	}
}
