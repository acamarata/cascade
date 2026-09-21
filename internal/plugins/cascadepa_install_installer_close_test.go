package plugins

// Purpose (this file): mapAddOutcome's fail-closed default and
//   installerAdapter.Close, split out of
//   cascadepa_install_installer_elevation_test.go to keep every sibling
//   file under the 300-line cap.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// TestMapAddOutcome_UnknownOutcomeRefuses is the round-1 CR fix item 7
// proof: an outcome mapAddOutcome does not recognize is refused, never
// silently reported as a successful install (the old default was fail-open).
func TestMapAddOutcome_UnknownOutcomeRefuses(t *testing.T) {
	_, err := mapAddOutcome(AddOutcome(99))
	if err == nil {
		t.Fatal("mapAddOutcome(99): err = nil, want a refusal for an unrecognized AddOutcome")
	}
}

// TestInstallerAdapter_Close_ReleasesRealHandles proves Close (round-1 CR
// fix item 11) actually releases the raw *sql.DB handle init opened.
//
// REWORK (round-3, T0 decision D1): round-2 also closed the shared
// sharedCascadeStore here and asserted a SECOND Add failed as a result --
// that relied on shared.Close() closing an owned, private driver, which
// D1 deleted: the shared store now only ever returns the daemon's own
// injected Store, and Close on it is a PERMANENT no-op (it must never
// close a store every other adapter shares). This test now asserts Close
// directly on the raw *sql.DB init opened (a.db, unexported, same-package
// white-box access) -- the handle this adapter genuinely owns.
func TestInstallerAdapter_Close_ReleasesRealHandles(t *testing.T) {
	dir := t.TempDir()
	resolvePaths := func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil }
	hostStoreFixture(t, dir)
	a := newInstallerAdapter(resolvePaths, testkit.NewFrozenClock(fixedInstallTestTime), newSharedCascadeStore())
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "already-here", Source: plugin.CandidateSourceInstalled,
		Manifest: plugin.Manifest{ID: "already-here", Runtime: plugin.RuntimeBuiltin},
	}}
	if _, err := a.Add(context.Background(), req); err != nil {
		t.Fatalf("Add before Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("installerAdapter.Close: %v", err)
	}
	if err := a.db.Ping(); err == nil {
		t.Fatal("a.db.Ping() after Close: err = nil, want the closed-handle failure -- Close did not release the raw *sql.DB")
	}
}

// failingCloser is an io.Closer double that always errors, so
// TestInstallerAdapter_Close_JoinsUnderlyingErrors can drive Close's
// errors.Join branch without depending on a real handle's own failure
// mode.
type failingCloser struct{ err error }

func (f failingCloser) Close() error { return f.err }

// TestInstallerAdapter_Close_JoinsUnderlyingErrors proves Close reports a
// closer's failure rather than swallowing it.
func TestInstallerAdapter_Close_JoinsUnderlyingErrors(t *testing.T) {
	a := newTestInstallerAdapter(t, "", nil, nil)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "already-here", Source: plugin.CandidateSourceInstalled,
		Manifest: plugin.Manifest{ID: "already-here", Runtime: plugin.RuntimeBuiltin},
	}}
	if _, err := a.Add(context.Background(), req); err != nil {
		t.Fatalf("Add: %v", err)
	}
	wantErr := errors.New("boom: close failed")
	a.closers = append(a.closers, failingCloser{err: wantErr})
	if err := a.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("Close: err = %v, want it to wrap %v", err, wantErr)
	}
}
