package topics

// Purpose: pins testdata/acceptance/four-topic-conversation.jsonl to
//   syntheticCorpus() (segmenter_harness_test.go) byte for byte, and checks
//   four-topic-live.jsonl opens with its provenance header (P1-CPA-12).
// Inputs: the two files under testdata/acceptance/.
// Outputs: none (t.Fatal only).
// Constraints: the encoding is one json.Marshal(CorpusRecord) per line,
//   newline-terminated, in syntheticCorpus order. A change to either side
//   without the other is red.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// encodeCorpusJSONL is the fixture's encoding of c.
func encodeCorpusJSONL(t *testing.T, c Corpus) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, rec := range c {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal %s: %v", rec.ID, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func TestFourTopicFixtureMatchesSyntheticCorpus(t *testing.T) {
	got, err := os.ReadFile(filepath.Join("testdata", "acceptance", "four-topic-conversation.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	want := encodeCorpusJSONL(t, syntheticCorpus())
	if !bytes.Equal(got, want) {
		t.Fatalf("four-topic-conversation.jsonl (%d bytes) differs from syntheticCorpus (%d bytes)", len(got), len(want))
	}
	topicsSeen := map[string]bool{}
	for i, line := range bytes.Split(bytes.TrimSuffix(got, []byte("\n")), []byte("\n")) {
		rec, err := LoadCorpusRecord(line)
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		for _, label := range rec.TopicLabels {
			topicsSeen[label] = true
		}
	}
	if len(topicsSeen) != 4 {
		t.Fatalf("fixture spans %d topics %v, want 4", len(topicsSeen), topicsSeen)
	}
}

// liveFixtureHeader is four-topic-live.jsonl's first line.
type liveFixtureHeader struct {
	Provenance struct {
		Source   string `json:"source"`
		Tool     string `json:"tool"`
		Date     string `json:"date"`
		Recorder string `json:"recorder"`
		Topics   int    `json:"topics"`
	} `json:"provenance"`
}

// liveFixtureTurn is every later line of four-topic-live.jsonl.
type liveFixtureTurn struct {
	Role  string `json:"role"`
	Text  string `json:"text"`
	Topic string `json:"topic"`
}

func TestFourTopicLiveFixtureHasProvenance(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "acceptance", "four-topic-live.jsonl"))
	if err != nil {
		t.Fatalf("open live fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("live fixture is empty")
	}
	var head liveFixtureHeader
	if err := json.Unmarshal(sc.Bytes(), &head); err != nil || head.Provenance.Source == "" ||
		head.Provenance.Recorder == "" || head.Provenance.Date == "" || head.Provenance.Topics != 4 {
		t.Fatalf("header = %s (%v), want a provenance object naming source, recorder, date and 4 topics", sc.Text(), err)
	}
	topics, turns := map[string]bool{}, 0
	for sc.Scan() {
		var turn liveFixtureTurn
		if err := json.Unmarshal(sc.Bytes(), &turn); err != nil || turn.Text == "" || turn.Topic == "" {
			t.Fatalf("turn line %q: %v", sc.Text(), err)
		}
		topics[turn.Topic] = true
		turns++
	}
	if len(topics) != 4 || turns < 8 {
		t.Fatalf("live fixture: %d turns over %d topics, want at least 8 over 4", turns, len(topics))
	}
}
