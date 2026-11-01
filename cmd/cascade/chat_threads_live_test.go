//go:build live && !windows

package main

// Purpose: P1-CPA-12's live proof. A real daemon, built from this tree and
//   run under a throwaway HOME and CASCADE_HOME, gets two gemini lanes from
//   `cascade provider add --key-env` (the embedding lane named by
//   CASCADE_LIVE_EMBED_LANE, and topics-classifier), [retrieval.embedding]
//   and [topics] config, and one internal thread replaying
//   four-topic-live.jsonl. `cascade chat --threads --json` must then list
//   exactly four threads whose turns are the fixture's four topic groups.
// Constraints: without CASCADE_LIVE_EMBED_LANE the test skips and names it
//   (CI keeps the skip). With it set, a missing GEMINI_FREE_KEY_1 FAILS.
//   The key reaches only the `provider add` child, by --key-env; it is
//   never printed, logged or passed to the daemon.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
)

const liveKeyVar = "GEMINI_FREE_KEY_1"

// liveRig is one throwaway machine: binary, env, socket.
type liveRig struct {
	bin, home, sock string
	env             []string
}

func newLiveRig(t *testing.T) *liveRig {
	t.Helper()
	root := t.TempDir()
	short, err := os.MkdirTemp("", "cpa12")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	r := &liveRig{bin: filepath.Join(root, "cascade"), home: filepath.Join(root, "home"), sock: filepath.Join(short, "d.sock")}
	build := exec.Command("go", "build", "-tags", "devkeys", "-o", r.bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	ch := filepath.Join(r.home, ".cascade")
	if err := os.MkdirAll(ch, 0o700); err != nil {
		t.Fatal(err)
	}
	r.env = []string{"HOME=" + r.home, "USERPROFILE=" + r.home, "CASCADE_HOME=" + ch, "CASCADE_SOCKET=" + r.sock,
		"CASCADE_TEST_FILE_VAULT=1", "CASCADE_NO_INPUT=1", "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	return r
}

// run runs the binary with extra env appended; the output is returned, never
// echoed with the env.
func (r *liveRig) run(t *testing.T, extra []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(r.bin, args...) //nolint:gosec // built above.
	cmd.Dir, cmd.Env = r.home, append(append([]string(nil), r.env...), extra...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.String() + errb.String(), err
	}
	return out.String(), nil
}

func (r *liveRig) start(t *testing.T) {
	t.Helper()
	cmd := exec.Command(r.bin, "daemon", "run") //nolint:gosec // built above.
	cmd.Dir, cmd.Env = r.home, r.env
	if err := cmd.Start(); err != nil {
		t.Fatalf("daemon run: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for deadline := time.Now().Add(120 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if _, err := r.run(t, nil, "status"); err == nil {
			return
		}
	}
	t.Fatal("the daemon never answered `cascade status`")
}

func (r *liveRig) rpc(t *testing.T, method string, params, out any) {
	t.Helper()
	c := client.New(r.sock, client.UnixDialer, 120*time.Second)
	if err := c.Do(context.Background(), method, params, out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

// liveTurn is one fixture turn (line 2 onward of four-topic-live.jsonl).
type liveTurn struct{ Role, Text, Topic string }

func loadLiveFixture(t *testing.T) []liveTurn {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "internal", "conversation", "topics", "testdata", "acceptance", "four-topic-live.jsonl"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Scan() // the provenance header
	var turns []liveTurn
	for sc.Scan() {
		var lt liveTurn
		if err := json.Unmarshal(sc.Bytes(), &lt); err != nil {
			t.Fatalf("fixture line: %v", err)
		}
		turns = append(turns, lt)
	}
	return turns
}

// requireLiveEnv skips without CASCADE_LIVE_EMBED_LANE and fails without
// the key; it returns the embedding lane name and the key.
func requireLiveEnv(t *testing.T) (string, string) {
	t.Helper()
	embedLane := os.Getenv("CASCADE_LIVE_EMBED_LANE")
	if embedLane == "" {
		t.Skip("CASCADE_LIVE_EMBED_LANE is unset: this live proof needs a real gemini embedding lane")
	}
	key := os.Getenv(liveKeyVar)
	if key == "" {
		t.Fatalf("%s is not exported: the live check cannot pass without the build session's key", liveKeyVar)
	}
	return embedLane, key
}

// liveLanes adds the two lanes and writes the config.
func liveLanes(t *testing.T, r *liveRig, embedLane, key string) {
	t.Helper()
	for _, lane := range []string{embedLane, "topics-classifier"} {
		if out, err := r.run(t, []string{liveKeyVar + "=" + key}, "provider", "add", lane, "--key-env", liveKeyVar, "--kind", "gemini"); err != nil {
			t.Fatalf("provider add %s: %v\n%s", lane, err, strings.ReplaceAll(out, key, "[redacted]"))
		}
	}
	cfg := "[retrieval.embedding]\nlane = \"" + embedLane + "\"\nmodel = \"gemini-embedding-001\"\ndimensions = 3072\n\n" +
		"[topics]\nclassifier_lane = \"topics-classifier\"\nclassifier_model = \"gemini-2.5-flash\"\n"
	if err := os.WriteFile(filepath.Join(r.home, ".cascade", "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// replayInternalThread appends every fixture turn to one internal thread.
func replayInternalThread(t *testing.T, r *liveRig, turns []liveTurn) {
	t.Helper()
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	r.rpc(t, "chat.create_thread", map[string]string{"privacy_mode": "internal"}, &created)
	for i, lt := range turns {
		var res struct {
			TopicWarning string `json:"topic_warning"`
		}
		r.rpc(t, "chat.append_turn", map[string]any{"thread_id": created.ThreadID, "role": lt.Role,
			"segments": []map[string]string{{"kind": "text", "content": lt.Text}}}, &res)
		if res.TopicWarning != "" {
			t.Fatalf("turn %d: topic_warning %q, want the engine to file it", i, res.TopicWarning)
		}
	}
}

func TestChatThreadsLiveFourFixtureThreads(t *testing.T) {
	embedLane, key := requireLiveEnv(t)
	r := newLiveRig(t)
	liveLanes(t, r, embedLane, key)
	r.start(t)
	fixture := loadLiveFixture(t)
	replayInternalThread(t, r, fixture)

	out, err := r.run(t, nil, "chat", "--threads", "--json")
	if err != nil {
		t.Fatalf("cascade chat --threads --json: %v\n%s", err, out)
	}
	var listed struct {
		Threads []struct{ ID string } `json:"threads"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(listed.Threads) != 4 {
		t.Fatalf("chat --threads listed %d threads, want exactly 4: %s", len(listed.Threads), out)
	}
	want := map[string][]string{}
	for _, lt := range fixture {
		want[lt.Topic] = append(want[lt.Topic], lt.Text)
	}
	matched := map[string]bool{}
	for _, th := range listed.Threads {
		var got struct {
			Turns []struct{ Segments []struct{ Content string } }
		}
		r.rpc(t, "chat.get_thread", map[string]string{"thread_id": th.ID}, &got)
		texts := make([]string, 0, len(got.Turns))
		for _, turn := range got.Turns {
			for _, s := range turn.Segments {
				texts = append(texts, s.Content)
			}
		}
		topic := topicFor(want, texts)
		if topic == "" || matched[topic] {
			t.Fatalf("thread %s turns %q match no unclaimed fixture topic", th.ID, texts)
		}
		matched[topic] = true
	}
}

// topicFor returns the fixture topic whose turns are exactly texts.
func topicFor(want map[string][]string, texts []string) string {
	got := append([]string(nil), texts...)
	sort.Strings(got)
	for topic, w := range want {
		exp := append([]string(nil), w...)
		sort.Strings(exp)
		if strings.Join(exp, "\x00") == strings.Join(got, "\x00") {
			return topic
		}
	}
	return ""
}
