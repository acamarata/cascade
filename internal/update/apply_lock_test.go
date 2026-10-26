package update

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// switchingFetcher serves first until the checksums file was fetched once
// (the plan), then second (the download): a release replaced at the same
// tag between Check and the download.
type switchingFetcher struct {
	first, second Fetcher
	mu            sync.Mutex
	sums          int
}

func (s *switchingFetcher) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	s.mu.Lock()
	if name == checksumsName {
		s.sums++
	}
	f := s.first
	if s.sums > 1 {
		f = s.second
	}
	s.mu.Unlock()
	return f.Get(ctx, name)
}

// TestApplyRefusesReleaseChangedAfterCheck: the download fetch serves a
// validly signed older set, or the same version with another digest, after
// the plan chose a newer one: refused, nothing written, no signal.
func TestApplyRefusesReleaseChangedAfterCheck(t *testing.T) {
	for _, tc := range []struct {
		name, version, msg string
		members            []member
	}{
		{name: "older than running", version: "1.9.0", msg: "downgrade"},
		{name: "older than planned", version: "2.0.1", msg: "changed between"},
		{name: "same version, other digest", version: "2.0.2", msg: "changed between",
			members: append(defaultMembers("2.0.2"), member{name: "EXTRA", body: "x"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "2.0.0", "2.0.2")
			second := t.TempDir()
			writeDist(t, second, distSpec{version: tc.version, key: f.key, members: tc.members, tag: "v2.0.2"})
			before := treeSum(t, f.home)
			d := f.deps("2.0.0", &switchingFetcher{first: DirFetcher{Dir: f.dist}, second: DirFetcher{Dir: second}})
			signals := 0
			d.Daemon.Status = func(context.Context) (bool, int, error) { return true, 7, nil }
			d.Daemon.Signal = func(int) error { signals++; return nil }
			res, err := Apply(context.Background(), d)
			wantKind(t, err, cascade.KindIntegrity, tc.msg)
			if res.Updated || signals != 0 {
				t.Fatalf("apply = %+v, %d signals", res, signals)
			}
			if treeSum(t, f.home) != before {
				t.Fatal("a release changed after the check wrote to the home tree")
			}
		})
	}
}

// TestApplyRefusesOlderThanInstalledReceipt: another updater installed a
// newer release than the running binary; a signed release between the two
// is a downgrade of the install and refuses.
func TestApplyRefusesOlderThanInstalledReceipt(t *testing.T) {
	f := newFixture(t, "2.0.2", "2.0.1")
	before := treeSum(t, f.home)
	_, err := Apply(context.Background(), f.deps("2.0.0", nil))
	wantKind(t, err, cascade.KindIntegrity, "older than the installed v2.0.2")
	if treeSum(t, f.home) != before {
		t.Fatal("a refused downgrade of the install wrote to the home tree")
	}
}

// blockingFetcher parks the archive download until release is closed.
type blockingFetcher struct {
	Fetcher
	archive          string
	entered, release chan struct{}
	once             sync.Once
}

func (b *blockingFetcher) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	if name == b.archive {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return b.Fetcher.Get(ctx, name)
}

// TestApplyRefusesConcurrentSelfUpdate: while one Apply holds the
// self-update lock (parked in its download), a second refuses with
// KindConflict and --check still answers without taking the lock.
func TestApplyRefusesConcurrentSelfUpdate(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	bf := &blockingFetcher{Fetcher: DirFetcher{Dir: f.dist}, archive: f.archiveName,
		entered: make(chan struct{}), release: make(chan struct{})}
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Apply(context.Background(), f.deps("2.0.0", bf))
		done <- outcome{res, err}
	}()
	<-bf.entered
	_, err := Apply(context.Background(), f.deps("2.0.0", nil))
	wantKind(t, err, cascade.KindConflict, "another self-update is running")
	if res, err := Check(context.Background(), f.deps("2.0.0", nil)); err != nil || res.Action != ActionUpdate {
		t.Fatalf("check while an update runs = %+v, %v", res, err)
	}
	close(bf.release)
	first := <-done
	if first.err != nil || !first.res.Updated || f.read("cascade") != "cascade 2.0.1" {
		t.Fatalf("the lock holder = %+v, %v", first.res, first.err)
	}
	if _, err := os.Lstat(filepath.Join(f.dataDir, "update", "lock")); !os.IsNotExist(err) {
		t.Fatalf("lock file left after the update: %v", err)
	}
	if again, err := Apply(context.Background(), f.deps("2.0.1", nil)); err != nil || again.Action != ActionNone {
		t.Fatalf("apply after the lock was released = %+v, %v", again, err)
	}
}
