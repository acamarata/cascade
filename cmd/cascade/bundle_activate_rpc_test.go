//go:build !windows

package main

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func bundleActivate(t *testing.T, s *bundleService, repo, hash string) (bundleStatusReply, error) {
	t.Helper()
	out, err := s.activate(context.Background(), bundleRaw(t, bundleRefParams{RepoID: repo, Hash: hash}))
	if err != nil {
		return bundleStatusReply{}, err
	}
	return out.(bundleStatusReply), nil
}

func bundleStatus(t *testing.T, s *bundleService, repo string) (bundleStatusReply, error) {
	t.Helper()
	out, err := s.status(context.Background(), bundleRaw(t, bundleStatusParams{RepoID: repo}))
	if err != nil {
		return bundleStatusReply{}, err
	}
	return out.(bundleStatusReply), nil
}

func bundleRollback(t *testing.T, s *bundleService, repo, hash string) error {
	t.Helper()
	_, err := s.rollback(context.Background(), bundleRaw(t, bundleRefParams{RepoID: repo, Hash: hash}))
	return err
}

// TestBundleActivationConsumesApproval: no approval, another hash, an
// expired approval and a second activate of one approval are each
// KindPermissionDenied; a valid approval is consumed exactly once.
func TestBundleActivationConsumesApproval(t *testing.T) {
	s, clock := newTestBundleService(t, nil)
	const noPending = "bundle.activate: no pending approval for this repository"

	_, err := bundleActivate(t, s, "repo-1", bundleHashA)
	requireBundleErr(t, err, cascade.KindPermissionDenied, noPending)

	s.approve("repo-1", bundleHashA, "v1")
	_, err = bundleActivate(t, s, "repo-1", bundleHashB)
	requireBundleErr(t, err, cascade.KindPermissionDenied, "bundle.activate: the approval covers another hash")
	_, err = bundleActivate(t, s, "repo-1", bundleHashA)
	requireBundleErr(t, err, cascade.KindPermissionDenied, noPending)

	s.approve("repo-1", bundleHashA, "v1")
	clock.Advance(bundleApprovalTTL)
	_, err = bundleActivate(t, s, "repo-1", bundleHashA)
	requireBundleErr(t, err, cascade.KindPermissionDenied, "bundle.activate: the approval has expired")

	s.approve("repo-1", bundleHashA, "v1")
	clock.Advance(bundleApprovalTTL - time.Second)
	got, err := bundleActivate(t, s, "repo-1", bundleHashA)
	if err != nil {
		t.Fatalf("valid activate: %v", err)
	}
	if got.InstalledHash != bundleHashA || got.PreviousHash != "" || got.PolicyVersion != "v1" || got.RolledBack {
		t.Fatalf("activate reply %+v", got)
	}
	_, err = bundleActivate(t, s, "repo-1", bundleHashA)
	requireBundleErr(t, err, cascade.KindPermissionDenied, noPending)
}

// TestBundleStatusReportsHashes: status returns installed_hash,
// previous_hash, policy_version and rolled_back, keyed by repo_id, or
// KindNotFound.
func TestBundleStatusReportsHashes(t *testing.T) {
	s, _ := newTestBundleService(t, nil)
	_, err := bundleStatus(t, s, "repo-1")
	requireBundleErr(t, err, cascade.KindNotFound, "bundle: no installed bundle for this repository")

	s.approve("repo-1", bundleHashA, "v1")
	if _, err := bundleActivate(t, s, "repo-1", bundleHashA); err != nil {
		t.Fatal(err)
	}
	s.approve("repo-1", bundleHashB, "v2")
	if _, err := bundleActivate(t, s, "repo-1", bundleHashB); err != nil {
		t.Fatal(err)
	}
	got, err := bundleStatus(t, s, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	want := bundleStatusReply{RepoID: "repo-1", InstalledHash: bundleHashB, PreviousHash: bundleHashA, PolicyVersion: "v2"}
	if got != want {
		t.Fatalf("status %+v, want %+v", got, want)
	}
	_, err = bundleStatus(t, s, "repo-2")
	requireBundleErr(t, err, cascade.KindNotFound, "bundle: no installed bundle for this repository")
	s.approve("repo-2", bundleHashA, "v9")
	if _, err := bundleActivate(t, s, "repo-2", bundleHashA); err != nil {
		t.Fatal(err)
	}
	if again, _ := bundleStatus(t, s, "repo-1"); again != want {
		t.Fatalf("repo-2 changed repo-1's record: %+v", again)
	}
	_, err = bundleStatus(t, s, "../repo-1")
	requireBundleErr(t, err, cascade.KindInvalidInput, "bundle: repo_id is malformed")
}

// recordingBundleAudit records appends and checks rolled_back is still false
// on disk when the event is appended.
type recordingBundleAudit struct {
	t      *testing.T
	s      *bundleService
	err    error
	events []audit.Event
}

func (a *recordingBundleAudit) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	rec, err := a.s.load("repo-1")
	if err != nil || rec.RolledBack {
		a.t.Errorf("audit append saw record %+v, %v: rolled_back set before the event", rec, err)
	}
	if a.err != nil {
		return audit.Record{}, a.err
	}
	a.events = append(a.events, e)
	return audit.Record{Event: e}, nil
}

// TestBundleRollbackVerbRules: another hash, after the window and a second
// rollback are KindPermissionDenied; a success appends exactly one
// KindConfigReload event before rolled_back is set; an audit error is
// returned with rolled_back false; the next activate clears it.
func TestBundleRollbackVerbRules(t *testing.T) {
	aud := &recordingBundleAudit{t: t}
	s, clock := newTestBundleService(t, aud)
	aud.s = s
	activate := func(hash string) {
		t.Helper()
		s.approve("repo-1", hash, "v1")
		if _, err := bundleActivate(t, s, "repo-1", hash); err != nil {
			t.Fatal(err)
		}
	}
	activate(bundleHashA)
	requireBundleErr(t, bundleRollback(t, s, "repo-1", bundleHashB), cascade.KindPermissionDenied, "bundle.rollback: another hash is installed")

	injected := cascade.New(cascade.KindUnavailable, "audit store down")
	aud.err = injected
	if err := bundleRollback(t, s, "repo-1", bundleHashA); err != injected {
		t.Fatalf("audit error: got %v, want the injected error", err)
	}
	if st, _ := bundleStatus(t, s, "repo-1"); st.RolledBack {
		t.Fatal("rolled_back set after a failed audit append")
	}
	aud.err = nil

	if err := bundleRollback(t, s, "repo-1", bundleHashA); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(aud.events) != 1 || aud.events[0].Kind != audit.KindConfigReload || aud.events[0].Action != bundleRollbackMethod {
		t.Fatalf("audit events %+v, want one config.reload bundle.rollback", aud.events)
	}
	if st, _ := bundleStatus(t, s, "repo-1"); !st.RolledBack {
		t.Fatal("rolled_back not set after rollback")
	}
	requireBundleErr(t, bundleRollback(t, s, "repo-1", bundleHashA), cascade.KindPermissionDenied, "bundle.rollback: already rolled back")
	if len(aud.events) != 1 {
		t.Fatalf("a refused rollback appended: %d events", len(aud.events))
	}

	activate(bundleHashB)
	if st, _ := bundleStatus(t, s, "repo-1"); st.RolledBack || st.InstalledHash != bundleHashB {
		t.Fatalf("activate did not clear rolled_back: %+v", st)
	}
	clock.Advance(bundleApprovalTTL)
	requireBundleErr(t, bundleRollback(t, s, "repo-1", bundleHashB), cascade.KindPermissionDenied, "bundle.rollback: the rollback window has closed")
	if len(aud.events) != 1 {
		t.Fatalf("a refused rollback appended: %d events", len(aud.events))
	}
}
