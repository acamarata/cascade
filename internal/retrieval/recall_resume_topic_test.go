//go:build integration && live && !windows

// Purpose: P1-CPA-12's resume proof over the production embedder. Its own
// daemon (acceptanceBinary, contract:recall-acceptance-harness) runs over its
// own CASCADE_HOME with two gemini lanes, [retrieval.embedding] and [topics];
// one internal thread replays four-topic-live.jsonl, the engine auto-files
// it, `recall what` resolves an auto-filed topic thread and `chat
// --resume-topic` resumes it into one new session.
// Constraints: skips naming CASCADE_LIVE_EMBED_LANE when it is unset; a
// missing GEMINI_FREE_KEY_1 fails. The key reaches only the `provider add`
// child, by --key-env, and is never printed. The daemon stops in t.Cleanup
// (startDaemon).
// SPORT: internal.retrieval recall resume topic (ADD) (P1-CPA-12).
package retrieval_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const resumeKeyVar = "GEMINI_FREE_KEY_1"

// addLiveLane runs `cascade provider add <lane> --key-env` with the key in
// that child's env only.
func (e *acceptEnv) addLiveLane(t *testing.T, lane, key string) {
	t.Helper()
	cmd := exec.Command(acceptanceBinary, "provider", "add", lane, "--key-env", resumeKeyVar, "--kind", "gemini") //nolint:gosec // built by TestMain.
	cmd.Dir, cmd.Env = e.work, append(append([]string(nil), e.env...), resumeKeyVar+"="+key)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("provider add %s: %v\n%s", lane, err, strings.ReplaceAll(string(out), key, "[redacted]"))
	}
}

// replayLiveFixture appends four-topic-live.jsonl to one internal thread and
// returns its id and the baking turns' texts.
func (e *acceptEnv) replayLiveFixture(t *testing.T) (string, []string) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "conversation", "topics", "testdata", "acceptance", "four-topic-live.jsonl"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	e.rpc(t, "chat.create_thread", map[string]string{"privacy_mode": "internal", "cwd": e.work}, &created)
	sc := bufio.NewScanner(f)
	sc.Scan() // provenance header
	var baking []string
	for sc.Scan() {
		var turn struct{ Role, Text, Topic string }
		if err := json.Unmarshal(sc.Bytes(), &turn); err != nil {
			t.Fatalf("fixture line: %v", err)
		}
		var res struct {
			TopicWarning string `json:"topic_warning"`
		}
		e.rpc(t, "chat.append_turn", map[string]any{"thread_id": created.ThreadID, "role": turn.Role,
			"segments": []map[string]string{{"kind": "text", "content": turn.Text}}}, &res)
		if res.TopicWarning != "" {
			t.Fatalf("topic_warning %q, want the engine to file the turn", res.TopicWarning)
		}
		if turn.Topic == "baking" {
			baking = append(baking, turn.Text)
		}
	}
	return created.ThreadID, baking
}

// topicThreadIDs lists the auto-filed topic threads (chat.threads_list).
func (e *acceptEnv) topicThreadIDs(t *testing.T) []string {
	t.Helper()
	var res struct {
		Threads []struct {
			ID string `json:"id"`
		} `json:"threads"`
	}
	e.rpc(t, "chat.threads_list", map[string]int{"page": 1, "page_size": 50}, &res)
	ids := make([]string, 0, len(res.Threads))
	for _, th := range res.Threads {
		ids = append(ids, th.ID)
	}
	return ids
}

// appendEmbedTopicsConfig adds the production embedder and the topic
// classifier, both on gemini lanes, to the daemon config under home.
func appendEmbedTopicsConfig(t *testing.T, home, lane string) {
	t.Helper()
	cfgPath := filepath.Join(home, ".cascade", "config.toml")
	base, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	extra := "\n[retrieval.embedding]\nlane = \"" + lane + "\"\nmodel = \"gemini-embedding-001\"\ndimensions = 3072\n\n" +
		"[topics]\nclassifier_lane = \"topics-classifier\"\nclassifier_model = \"gemini-2.5-flash\"\n"
	if err := os.WriteFile(cfgPath, append(base, extra...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecallResumeTopicResolutionWithProductionEmbedder(t *testing.T) {
	lane := os.Getenv("CASCADE_LIVE_EMBED_LANE")
	if lane == "" {
		t.Skip("CASCADE_LIVE_EMBED_LANE is unset: this live proof needs a real gemini embedding lane")
	}
	key := os.Getenv(resumeKeyVar)
	if key == "" {
		t.Fatalf("%s is not exported: the live check cannot pass without the build session's key", resumeKeyVar)
	}
	e := newAcceptEnv(t)
	e.addLiveLane(t, lane, key)
	e.addLiveLane(t, "topics-classifier", key)
	appendEmbedTopicsConfig(t, e.home, lane)
	e.startDaemon(t)
	seed, baking := e.replayLiveFixture(t)

	topicsFiled := e.topicThreadIDs(t)
	if len(topicsFiled) == 0 {
		t.Fatal("the engine filed no topic thread")
	}
	env := decodeWhat(t, e.mustCLI(t, "", "recall", "what", "sourdough starter proofing", "--json"))
	topPath := ""
	for _, r := range env.Data.Results {
		for _, id := range topicsFiled {
			if r.Domain == "conversation" && id != seed && (r.ID == id || strings.Contains(r.Path, id)) && topPath == "" {
				topPath = r.Path
			}
		}
	}
	if topPath == "" {
		t.Fatalf("recall what resolved no auto-filed topic thread among %v: %+v", topicsFiled, env.Data.Results)
	}
	before := e.threadIDs(t)
	out, errb, code := e.cli(t, "", "chat", "--resume-topic", topPath, "--json", "-m", "continue")
	if kind := refusalKind(errb); code != 0 && kind != "unsupported" && kind != "unavailable" && kind != "not-found" {
		t.Fatalf("resume exited %d (%q)\nstdout: %s\nstderr: %s", code, kind, out, errb)
	}
	var added []string
	for id := range e.threadIDs(t) {
		if !before[id] {
			added = append(added, id)
		}
	}
	if len(added) != 1 {
		t.Fatalf("resume created %d session threads, want exactly 1: %v", len(added), added)
	}
	turns := e.turns(t, added[0])
	if len(turns) == 0 || len(turns[0].Segments) == 0 || !containsAny(turns[0].Segments[0].Content, baking) {
		t.Fatalf("the resumed session's first turn does not carry the auto-filed baking topic: %+v", turns)
	}
}

// containsAny reports whether s contains any of subs.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
