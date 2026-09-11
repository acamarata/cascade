package jobs

import (
	"context"
	"testing"
)

func baseLease(repoID, scopeGlob string) ResourceLease {
	return ResourceLease{
		RepoID: repoID, ScopeGlob: scopeGlob, Holder: "job-1",
		IssuedAt: 1, TTLSeconds: 60, RenewCount: 0, JournalRef: "journal:1",
		Epoch: 1, State: LeaseHeld,
	}
}

func TestPutAndGetLease(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	l := baseLease("repo-1", "src/**")
	if err := s.PutLease(ctx, l); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	got, ok, err := s.GetLease(ctx, "repo-1", "src/**")
	if err != nil || !ok {
		t.Fatalf("GetLease: got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.Holder != l.Holder || got.Epoch != l.Epoch || got.State != l.State {
		t.Errorf("GetLease = %+v, want %+v", got, l)
	}
}

// TestLeaseEpochColumn covers epoch monotonicity refusal and the closed
// lease-state decode.
func TestLeaseEpochColumn(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	l := baseLease("repo-2", "pkg/**")
	l.Epoch = 5
	if err := s.PutLease(ctx, l); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	raised := l
	raised.Epoch = 6
	if err := s.PutLease(ctx, raised); err != nil {
		t.Fatalf("PutLease(raised epoch): %v", err)
	}
	lowered := l
	lowered.Epoch = 4
	if err := s.PutLease(ctx, lowered); err == nil {
		t.Error("PutLease(lowered epoch) = nil, want typed monotonicity error")
	}
	got, _, err := s.GetLease(ctx, "repo-2", "pkg/**")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if got.Epoch != 6 {
		t.Errorf("GetLease epoch = %d, want unchanged 6 (refused lowering must not write)", got.Epoch)
	}
}

func TestPutLeaseRejectsUnknownState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	l := baseLease("repo-3", "docs/**")
	l.State = LeaseState("bogus")
	if err := s.PutLease(ctx, l); err == nil {
		t.Error("PutLease(unknown state) = nil, want error")
	}
}

func TestGetLeaseNotFoundIsNoErrorNoOK(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, ok, err := s.GetLease(ctx, "repo-never", "x/**")
	if err != nil {
		t.Fatalf("GetLease: err=%v, want nil", err)
	}
	if ok {
		t.Error("GetLease: ok=true, want false")
	}
}

func TestPutAndGetWorktree(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	l := baseLease("repo-4", "cmd/**")
	if err := s.PutLease(ctx, l); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	w := Worktree{Path: "/tmp/worktrees/job-1", LeaseRepoID: "repo-4", LeaseScopeGlob: "cmd/**", Repo: "cascade", Branch: "job/1"}
	if err := s.PutWorktree(ctx, w); err != nil {
		t.Fatalf("PutWorktree: %v", err)
	}
	got, ok, err := s.GetWorktree(ctx, w.Path)
	if err != nil || !ok {
		t.Fatalf("GetWorktree: got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.Branch != w.Branch || got.Repo != w.Repo {
		t.Errorf("GetWorktree = %+v, want %+v", got, w)
	}
}

func TestPutWorktreeRefusesMissingLease(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	w := Worktree{Path: "/tmp/worktrees/orphan", LeaseRepoID: "repo-none", LeaseScopeGlob: "x/**", Repo: "cascade", Branch: "job/none"}
	if err := s.PutWorktree(ctx, w); err == nil {
		t.Error("PutWorktree(missing lease ref) = nil, want typed error")
	}
}

func TestPutLeaseRejectsEmptyKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutLease(ctx, ResourceLease{}); err == nil {
		t.Error("PutLease(empty repo_id/scope_glob) = nil, want error")
	}
}

func TestPutWorktreeRejectsEmptyFields(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutWorktree(ctx, Worktree{}); err == nil {
		t.Error("PutWorktree(empty fields) = nil, want error")
	}
}

func TestGetLeaseDecodeErrorOnBadStoredState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	l := baseLease("repo-5", "raw/**")
	if err := s.PutLease(ctx, l); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE `+tableResourceLease+` SET state = 'bogus' WHERE repo_id = ? AND scope_glob = ?`,
		l.RepoID, l.ScopeGlob); err != nil {
		t.Fatalf("corrupt lease state: %v", err)
	}
	if _, _, err := s.GetLease(ctx, l.RepoID, l.ScopeGlob); err == nil {
		t.Error("GetLease(corrupted state) = nil error, want typed decode error")
	}
}

func TestGetWorktreeNotFoundIsNoErrorNoOK(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, ok, err := s.GetWorktree(ctx, "/never/created")
	if err != nil || ok {
		t.Errorf("GetWorktree(missing) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}
