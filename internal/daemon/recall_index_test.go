package daemon

// Purpose: covers recall_index.go, the F/S-11.T4 daemon-side registration.
//   That ticket verified ./internal/retrieval/lifecycle/... and
//   ./cmd/cascade/... but never ./internal/daemon/..., so every function
//   in recall_index.go measured 0% and the package fell from 88.5% to
//   78.9%, under its 85 floor. Added by T0 (R-14.201) for the same reason
//   context_scope_test.go's header documents: new lines in this package
//   need real tests here, since cmd/cascade's tests do not contribute to
//   this package's own profile.
// Constraints: Art.2 -- the git helpers run against a REAL repository
//   under t.TempDir(), and the registration test drives the methods
//   through the REAL registry.Dispatch entry point. Art.7.1 -- no network
//   listener, nothing written outside t.TempDir().

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

// chdir moves the process into dir for the duration of the test.
// recallIndexRunGit resolves the repository from the working directory,
// so exercising it truthfully means being inside one.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// seedRepo makes dir a real git repository with one commit.
func seedRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@example.invalid")
	runGit(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-q", "-m", "seed")
}

func TestRecallIndexDataDirIsUnderDataDir(t *testing.T) {
	got := recallIndexDataDir(fakePaths{root: "/r"})
	if want := filepath.Join("/r", "data", "retrieval"); got != want {
		t.Fatalf("recallIndexDataDir = %q, want %q", got, want)
	}
}

// TestRegisterRecallIndexHandler_NilStoreRegistersNothing pins the
// documented nil-store contract: an unknown-method response is the honest
// answer for a build with no runtime store, so the namespace must be
// absent rather than registered against a nil manager.
func TestRegisterRecallIndexHandler_NilStoreRegistersNothing(t *testing.T) {
	reg := rpc.NewRegistry()
	if err := RegisterRecallIndexHandler(reg, fakePaths{root: t.TempDir()}, nil, nil, ""); err != nil {
		t.Fatalf("RegisterRecallIndexHandler(nil store) = %v, want nil", err)
	}
	for _, m := range []string{
		RecallIndexRebuildMethod, RecallIndexVerifyMethod,
		RecallIndexUpdateMethod, RecallIndexMigrateMethod,
	} {
		if reg.Registered(m) {
			t.Errorf("method %q registered despite a nil store", m)
		}
	}
}

func TestParseNameStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
		del  map[string]bool
	}{
		{name: "empty", in: "", want: nil},
		{name: "whitespace only", in: "   \n  \n", want: nil},
		{name: "short line ignored", in: "M\n", want: nil},
		{
			name: "sorted and deletion flagged",
			in:   "M\tz.go\nD\ta.go\nA\tm.go\n",
			want: []string{"a.go", "m.go", "z.go"},
			del:  map[string]bool{"a.go": true},
		},
		{
			name: "rename keeps the destination path",
			in:   "R100\told.go\tnew.go\n",
			want: []string{"new.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNameStatus(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseNameStatus(%q) = %+v, want %d entries", tc.in, got, len(tc.want))
			}
			for i, w := range tc.want {
				if got[i].Path != w {
					t.Errorf("entry %d path = %q, want %q", i, got[i].Path, w)
				}
				if got[i].Deleted != tc.del[w] {
					t.Errorf("entry %d (%s) Deleted = %v, want %v", i, w, got[i].Deleted, tc.del[w])
				}
			}
		})
	}
}

// TestGitTreeHashExec_RealRepositoryChangesWithTheWorktree proves the
// marker is worktree-sensitive, which is what lets drift be detected
// before a commit rather than only after one.
func TestGitTreeHashExec_RealRepositoryChangesWithTheWorktree(t *testing.T) {
	dir := t.TempDir()
	seedRepo(t, dir)
	chdir(t, dir)

	clean, err := gitTreeHashExec(context.Background())
	if err != nil {
		t.Fatalf("gitTreeHashExec: %v", err)
	}
	if clean == "" {
		t.Fatal("gitTreeHashExec returned an empty marker inside a real repository")
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := gitTreeHashExec(context.Background())
	if err != nil {
		t.Fatalf("gitTreeHashExec (dirty): %v", err)
	}
	if dirty == clean {
		t.Fatal("marker did not change after an uncommitted edit; drift would be undetectable until commit")
	}
}

// TestGitTreeHashExec_NoRepositoryIsSupported pins the documented
// fallback: no repository is a supported configuration, not an error.
func TestGitTreeHashExec_NoRepositoryIsSupported(t *testing.T) {
	chdir(t, t.TempDir())
	got, err := gitTreeHashExec(context.Background())
	if err != nil {
		t.Fatalf("gitTreeHashExec outside a repository = %v, want nil error", err)
	}
	if got != "" {
		t.Fatalf("gitTreeHashExec outside a repository = %q, want empty", got)
	}
}

func TestGitDiffExec(t *testing.T) {
	dir := t.TempDir()
	seedRepo(t, dir)
	chdir(t, dir)

	marker, err := gitTreeHashExec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "b.txt")
	runGit(t, dir, "commit", "-q", "-m", "add b")

	changed, newTree, err := gitDiffExec(context.Background(), marker)
	if err != nil {
		t.Fatalf("gitDiffExec: %v", err)
	}
	if newTree == "" {
		t.Error("gitDiffExec returned an empty new marker")
	}
	var found bool
	for _, c := range changed {
		if c.Path == "b.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("gitDiffExec did not report b.txt; got %+v", changed)
	}
}

// TestGitDiffExec_EmptyMarkerReportsNothing pins the first-run path: with
// no previous marker there is no commit to diff against, so the honest
// answer is "nothing to report" plus the current marker, never an error.
func TestGitDiffExec_EmptyMarkerReportsNothing(t *testing.T) {
	dir := t.TempDir()
	seedRepo(t, dir)
	chdir(t, dir)

	changed, newTree, err := gitDiffExec(context.Background(), "")
	if err != nil {
		t.Fatalf("gitDiffExec(\"\") = %v, want nil", err)
	}
	if len(changed) != 0 {
		t.Errorf("gitDiffExec(\"\") reported %+v, want nothing", changed)
	}
	if newTree == "" {
		t.Error("gitDiffExec(\"\") returned an empty marker inside a real repository")
	}
}

// TestGitDiffExec_UnknownCommitIsNotAnError pins the documented
// treatment of a marker naming a commit this repository does not have.
func TestGitDiffExec_UnknownCommitIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	seedRepo(t, dir)
	chdir(t, dir)

	changed, _, err := gitDiffExec(context.Background(), "0000000000000000000000000000000000000000:x")
	if err != nil {
		t.Fatalf("gitDiffExec(unknown commit) = %v, want nil", err)
	}
	if len(changed) != 0 {
		t.Errorf("gitDiffExec(unknown commit) reported %+v, want nothing", changed)
	}
}

func TestRecallIndexRunGit_ErrorsOutsideRepository(t *testing.T) {
	chdir(t, t.TempDir())
	if _, err := recallIndexRunGit(context.Background(), "rev-parse", "HEAD"); err == nil {
		t.Fatal("recallIndexRunGit outside a repository = nil error, want an error")
	}
}
