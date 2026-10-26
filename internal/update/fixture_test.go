package update

// Purpose: an installed-cascade fixture (install dir, receipt, data dir) and
//   a signed dist to update from, all under t.TempDir().

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/runtime"
)

type fixture struct {
	t                                         *testing.T
	home, installDir, dist, receipt, dataDir  string
	key                                       testKey
	clock                                     *runtime.FixedClock
	archiveName, archiveSHA, installedVersion string
}

// newFixture installs version installed (helper, plugin, cascade) and
// writes a signed dist of version served.
func newFixture(t *testing.T, installed, served string) *fixture {
	t.Helper()
	home := t.TempDir()
	f := &fixture{t: t, home: home, installDir: filepath.Join(home, ".local", "bin"), dist: filepath.Join(home, "dist"),
		receipt: filepath.Join(home, ".cascade", receiptFileName), dataDir: filepath.Join(home, ".cascade", "data"),
		key: newTestKey(1), clock: runtime.NewFixedClock(time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)), installedVersion: installed}
	must(t, os.MkdirAll(f.installDir, 0o755))
	must(t, os.MkdirAll(filepath.Dir(f.receipt), 0o755))
	var files []ReceiptFile
	for _, m := range defaultMembers(installed)[:3] {
		put(t, f.installDir, m.name, []byte(m.body))
		files = append(files, ReceiptFile{Name: m.name, SHA256: hexSum([]byte(m.body))})
	}
	r := Receipt{SchemaVersion: 1, Channel: "script", ReleaseTag: "v" + installed, ArchiveName: archiveNameFor(installed, testPlatform),
		ArchiveSHA256: hexSum([]byte("archive " + installed)), InstallDir: f.installDir, InstalledAt: "2026-01-01T00:00:00Z", Files: files}
	must(t, WriteReceipt(f.receipt, r))
	f.archiveName, f.archiveSHA = writeDist(t, f.dist, distSpec{version: served, key: f.key})
	return f
}

// countingFetcher records every name it serves.
type countingFetcher struct {
	Fetcher
	mu   sync.Mutex
	got  []string
	fail error
}

func (c *countingFetcher) Get(ctx context.Context, url string) (io.ReadCloser, error) {
	c.mu.Lock()
	c.got = append(c.got, url)
	c.mu.Unlock()
	if c.fail != nil {
		return nil, c.fail
	}
	return c.Fetcher.Get(ctx, url)
}

func (c *countingFetcher) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.got...)
}

// deps wires Deps for a running version, a fetcher over the dist and a
// daemon that is not running.
func (f *fixture) deps(running string, fetch Fetcher) Deps {
	if fetch == nil {
		fetch = DirFetcher{Dir: f.dist}
	}
	return Deps{
		Env:      Env{Executable: func() (string, error) { return filepath.Join(f.installDir, "cascade"), nil }, ReceiptPath: f.receipt},
		Version:  running,
		Source:   Source{Dist: f.dist},
		Fetcher:  fetch,
		Platform: testPlatform,
		TrustRoot: func() (minisign.PublicKey, error) {
			return f.key.pub(f.t), nil
		},
		DataDir: f.dataDir,
		Clock:   f.clock,
		Daemon: DrainDeps{
			Status: func(context.Context) (bool, int, error) { return false, 0, nil },
			Signal: func(int) error { f.t.Fatal("Signal called with no daemon running"); return nil },
		},
	}
}

func (f *fixture) read(name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.installDir, name))
	must(f.t, err)
	return string(b)
}

// treeSum hashes every path and file under dir, so any write shows.
func treeSum(t *testing.T, dir string) string {
	t.Helper()
	var paths []string
	must(t, filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		paths = append(paths, p)
		return err
	}))
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p + "\x00"))
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			must(t, err)
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
