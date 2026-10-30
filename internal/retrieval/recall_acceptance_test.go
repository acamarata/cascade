//go:build integration && !windows

// Purpose: the recall-to-resume end-to-end acceptance on a REAL daemon
// (P1-CPA-09, contract:recall-acceptance-harness). TestMain builds
// ./cmd/cascade once (devkeys, so the QA file vault binds) and exports the
// path as acceptanceBinary for P1-CPA-12. The root test seeds conversation,
// memory and files through the daemon's own surfaces, then drives the binary.
// A general session reads only non-project kinds (R-16.3, P1-CPA-20), so the
// seed memory is `--type user` and the configured (project-privacy) files
// source yields no hit: "more than one domain" is conversation plus memory.
// Constraints: no real HOME or keychain (isolatedEnv); each test owns its
// daemon and never reuses another's.
// SPORT: internal.retrieval recall_acceptance (ADD) (P1-CPA-09).
package retrieval_test

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// acceptanceBinary is the built cascade (contract:recall-acceptance-harness).
var acceptanceBinary string

func TestMain(m *testing.M) {
	if f := flag.Lookup("test.list"); f != nil && f.Value.String() != "" {
		os.Exit(m.Run()) // -list needs no binary.
	}
	dir, err := os.MkdirTemp("", "cpa09bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		os.Exit(1)
	}
	acceptanceBinary = filepath.Join(dir, "cascade")
	build := exec.Command("go", "build", "-tags", "devkeys", "-o", acceptanceBinary, "./cmd/cascade")
	build.Dir = "../.."
	if out, berr := build.CombinedOutput(); berr != nil {
		fmt.Fprintf(os.Stderr, "TestMain: building cascade: %v\n%s", berr, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	killDaemons()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestAcceptEnvIsolation proves the injected custody and isolation without
// launching a process: every child variable points under the temp tree, the
// file-vault switch is exactly "1", and the binary is a devkeys build.
func TestAcceptEnvIsolation(t *testing.T) {
	e := newAcceptEnv(t)
	want := map[string]string{"CASCADE_TEST_FILE_VAULT": "1", "CASCADE_NO_INPUT": "1"}
	got := map[string]string{}
	for _, kv := range e.env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		if !strings.HasPrefix(got[k], e.root) || got[k] == os.Getenv("HOME") {
			t.Errorf("%s = %q escapes the temp tree %q", k, got[k], e.root)
		}
	}
	if len(e.env) != 7 {
		t.Errorf("the child env carries %d variables, want exactly the 7 injected: %v", len(e.env), e.env)
	}
	out, err := exec.Command("go", "version", "-m", acceptanceBinary).CombinedOutput() //nolint:gosec // path from TestMain.
	if err != nil || !strings.Contains(string(out), "devkeys") {
		t.Errorf("the binary is not a devkeys build (the file-vault switch would not bind): %v\n%s", err, out)
	}
}

// acceptRun carries the state the steps hand to each other.
type acceptRun struct {
	e                      *acceptEnv
	seed                   acceptSeed
	convID, restricted     string
	topPath                string
	beforeTurns, beforeIDs int
}

// TestEpicV_RecallResumeAcceptance is the epic acceptance, Steps 1-4.
func TestEpicV_RecallResumeAcceptance(t *testing.T) {
	r := &acceptRun{e: newAcceptEnv(t), seed: loadSeed(t)}
	r.seedOffline(t)
	r.e.startDaemon(t) // on the parent t: the daemon outlives every step.
	t.Run("Step1", r.step1Seed)
	t.Run("Step2", r.step2Recall)
	t.Run("Step2b", r.step2bRestricted)
	t.Run("Step3", r.step3Resume)
	t.Run("Step4", r.step4LocalOnly)
}

// seedOffline writes the files source and the memory record before the daemon
// starts: the memory projection runs at daemon start and every 60 s
// (R-14.294), so a later record would not be searchable inside the test.
func (r *acceptRun) seedOffline(t *testing.T) {
	e, s := r.e, r.seed
	if err := os.WriteFile(filepath.Join(e.work, "files", "handbook.md"), []byte(s.File), 0o600); err != nil {
		t.Fatal(err)
	}
	e.mustCLI(t, "", "memory", "remember", "--type", "user", "--name", s.MemoryName, s.Memory)
}

// step1Seed seeds conversation and the vault canary over the live socket and
// builds the files index; the scope row is read back before Step 2.
func (r *acceptRun) step1Seed(t *testing.T) {
	e, s := r.e, r.seed
	e.mustCLI(t, s.VaultValue+"\n", "vault", "set", s.VaultName)
	if _, err := os.Stat(filepath.Join(e.home, ".cascade", "data", "vault.age")); err != nil {
		t.Fatalf("custody is not the QA file vault under CASCADE_HOME (the injection did not bind): %v", err)
	}
	r.convID = e.seedThread(t, "internal", s.Conversation)
	r.restricted = e.seedThread(t, "restricted", s.Restricted)
	e.mustCLI(t, "", "recall", "index", "rebuild")
	ref, found := e.projectRef(t, r.convID)
	if !found || ref != "" {
		t.Fatalf("conversation_thread_project row = (%q, found %v), want the general bucket \"\"", ref, found)
	}
	if got := e.turns(t, r.convID); len(got) != 1 || !strings.Contains(got[0].Segments[0].Content, "quartz lighthouse") {
		t.Fatalf("the seeded thread's stored turns are wrong: %+v", got)
	}
}

// step2Recall asserts the ranked multi-domain list and its schema.
func (r *acceptRun) step2Recall(t *testing.T) {
	out := r.e.mustCLI(t, "", "recall", "what", r.seed.Query, "--json")
	env := decodeWhat(t, out)
	if env.Version != 1 || !env.OK || env.Data.Query != r.seed.Query {
		t.Fatalf("envelope = version %d ok %v query %q", env.Version, env.OK, env.Data.Query)
	}
	res := env.Data.Results
	if len(res) < 2 {
		t.Fatalf("recall what returned %d results, want a ranked list over more than one domain:\n%s", len(res), out)
	}
	domains := map[string]bool{}
	for _, x := range res {
		if x.Domain == "" || x.ID == "" || x.Path == "" || x.CorpusID == "" {
			t.Errorf("result missing Domain/ID/Path/CorpusID: %+v", x)
		}
		if x.Domain == "file" || strings.Contains(x.Snippet, "checklist") {
			t.Errorf("a project-privacy file reached a general session: %+v", x)
		}
		domains[x.Domain] = true
	}
	if len(domains) < 2 || !domains["conversation"] || !domains["memory"] {
		t.Fatalf("domains = %v, want conversation and memory", domains)
	}
	if res[0].Domain != "conversation" {
		t.Fatalf("top result is %q, want conversation: %+v", res[0].Domain, res)
	}
	if res[0].Path != r.convID {
		t.Errorf("top path %q is not the seeded thread %q", res[0].Path, r.convID)
	}
	r.topPath = res[0].Path
}

// step2bRestricted: a default (restricted) thread never surfaces (R17 M-6).
func (r *acceptRun) step2bRestricted(t *testing.T) {
	if got := r.e.turns(t, r.restricted); len(got) != 1 || !strings.Contains(got[0].Segments[0].Content, "obsidian kestrel") {
		t.Fatalf("the restricted thread was not seeded: %+v", got)
	}
	out := r.e.mustCLI(t, "", "recall", "what", r.seed.RestrictedQuery, "--json")
	env := decodeWhat(t, out)
	if env.Data.Withheld < 1 {
		t.Fatalf("the restricted thread was never reached and withheld, the absence below is vacuous: %s", out)
	}
	for _, x := range env.Data.Results {
		if x.Path == r.restricted || strings.Contains(x.Snippet, "obsidian") {
			t.Fatalf("a restricted thread reached recall what: %+v", x)
		}
	}
	for _, c := range env.Data.Citations {
		if c.Path == r.restricted || c.ChunkID == r.restricted {
			t.Fatalf("a restricted thread leaked into the citations: %+v", c)
		}
	}
	if strings.Contains(env.Data.Rendered, r.restricted) {
		t.Fatalf("a restricted thread leaked into the rendered citations:\n%s", out)
	}
}
