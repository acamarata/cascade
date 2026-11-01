//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// factsCanary is a credential-shaped string the secret detector refuses.
func factsCanary() string { return "sk" + "-ant-" + strings.Repeat("Zq7", 10) }

// factsFixtureAdmission supplies the injected admission boundary when Git
// cannot execute. It scans the fixture bytes with the real secret detector;
// environments with Git exercise repo.AdmitBatch instead.
func factsFixtureAdmission(root string, rels []string) ([]repo.Admission, error) {
	det, err := secrets.ProcessDetector()
	if err != nil {
		return nil, err
	}
	out := make([]repo.Admission, len(rels))
	for i, rel := range rels {
		a := &out[i]
		a.Rel = rel
		if repo.ExcludedPath(rel) {
			a.Reason = repo.ReasonExcluded
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if len(det.Scan(content)) > 0 {
			a.Reason = repo.ReasonSecret
			continue
		}
		a.Admitted, a.Content = true, content
	}
	return out, nil
}

func TestFactsProposeSendsOnlyAdmittedContent(t *testing.T) {
	paths := factsTestEnv(t)
	const envBytes = "SERVICE_PASSWORD=envbytes-do-not-send"
	dir := factsGitRepo(t, map[string]string{
		".env":      envBytes + "\n",
		"README.md": "# widget\nkey " + factsCanary() + "\n",
		"go.mod":    "module example.com/widget-admitted\n\ngo 1.22\n",
	})
	exec := &factsRecorder{output: `{"proposals":[{"subject":"harness_files","fact":"none","confidence":0.5}]}`}
	var opened []string
	d := factsTestDeps(paths, exec, &opened)
	raw, _ := json.Marshal(map[string]string{"repo": dir})
	got, err := d.propose(context.Background(), raw)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if res := got.(factsProposeResult); res.Refused != 1 {
		t.Fatalf("refused = %d, want exactly the README refusal", res.Refused)
	}
	if slices.Contains(opened, ".env") || !slices.Equal(opened, []string{"README.md", "go.mod"}) {
		t.Fatalf("admission guard was handed %v, want only the candidates README.md and go.mod", opened)
	}
	if len(exec.reqs) != 1 {
		t.Fatalf("executor saw %d requests, want 1", len(exec.reqs))
	}
	sent, _ := json.Marshal(exec.reqs[0])
	if bytes.Contains(sent, []byte("envbytes-do-not-send")) || bytes.Contains(sent, []byte(factsCanary())) {
		t.Fatalf("refused or non-candidate bytes reached the executor: %s", sent)
	}
	if !bytes.Contains(sent, []byte("widget-admitted")) {
		t.Fatal("the admitted go.mod never reached the executor; the check above is vacuous")
	}
}

func TestFactsProposeTierFromTaskClass(t *testing.T) {
	paths := factsTestEnv(t)
	dir := factsGitRepo(t, map[string]string{"README.md": "# widget\n"})
	exec := &factsRecorder{output: `{"proposals":[]}`}
	d := factsTestDeps(paths, exec, new([]string))
	raw, _ := json.Marshal(map[string]string{"repo": dir, "tier": "public"})
	_, err := d.propose(context.Background(), raw)
	wantFactsKind(t, err, cascade.KindInvalidInput, "tier")
	if len(exec.reqs) != 0 {
		t.Fatal("a refused params set still reached the executor")
	}
	raw, _ = json.Marshal(map[string]string{"repo": dir})
	if _, err := d.propose(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var want provider.SensitivityTier
	found := false
	for _, row := range conductor.TaskClasses() {
		if row.Class == string(conductor.TaskClassExtract) {
			want, found = row.SensitivityDefault, true
		}
	}
	if !found {
		t.Fatal("the extract task class has no row")
	}
	if len(exec.reqs) != 1 {
		t.Fatalf("executor saw %d requests, want 1", len(exec.reqs))
	}
	if exec.reqs[0].Sensitivity != want || exec.reqs[0].TaskClass != "extract" {
		t.Fatalf("request sensitivity = %v, want the extract class default %v", exec.reqs[0].Sensitivity, want)
	}
}

func TestFactsProposeUnboundDoorWritesNothing(t *testing.T) {
	paths := factsTestEnv(t)
	dir := factsGitRepo(t, map[string]string{"README.md": "# widget\n"})
	d := factsTestDeps(paths, unreadyExecutor{&factsRecorder{}}, new([]string))
	raw, _ := json.Marshal(map[string]string{"repo": dir})
	_, err := d.propose(context.Background(), raw)
	wantFactsKind(t, err, cascade.KindUnavailable, "conductor door not bound")
	if _, statErr := os.Stat(filepath.Join(paths.DataDir(), "cascade.db")); !os.IsNotExist(statErr) {
		t.Fatalf("an unbound door still wrote cascade.db (stat err %v)", statErr)
	}
	d.Exec = nil
	_, err = d.propose(context.Background(), raw)
	wantFactsKind(t, err, cascade.KindInvalidInput, "non-nil model executor")
}

// unreadyExecutor is an executor whose Ready refuses like an unbound door.
type unreadyExecutor struct{ *factsRecorder }

func (unreadyExecutor) Ready() error {
	return cascade.New(cascade.KindUnavailable, "conductor door not bound")
}

func TestFactContentCandidatesAndCaps(t *testing.T) {
	for rel, want := range map[string]bool{
		"README": true, "README.md": true, "go.mod": true, "Makefile": true, ".github/workflows/ci.yml": true,
		"docs/README.md": false, ".env": false, "main.go": false, ".github/workflows/ci.yaml": false, "sub/go.mod": false,
	} {
		if factCandidate(rel) != want {
			t.Errorf("factCandidate(%q) = %v, want %v", rel, !want, want)
		}
	}
	big := bytes.Repeat([]byte("line of text\n"), 4000)
	capped := capFactContent(big, factContentFileCap)
	body, note, _ := bytes.Cut(capped, []byte("\n[cascade: truncated"))
	if len(body) > factContentFileCap || !bytes.HasSuffix(body, []byte("\n")) || len(note) == 0 {
		t.Fatalf("capped to %d bytes (note %q), want <= %d ending on a line", len(body), note, factContentFileCap)
	}
	if small := []byte("x\n"); !bytes.Equal(capFactContent(small, 10), small) {
		t.Fatal("content under the cap changed")
	}
}
