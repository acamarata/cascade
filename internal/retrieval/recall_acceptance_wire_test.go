//go:build integration && !windows

// Purpose: the wire shapes and daemon-surface helpers of the recall-to-resume
// acceptance (P1-CPA-09): the versioned `recall what --json` envelope decoded
// STRICTLY (an unknown or missing field fails), thread seeding and reading
// over the live socket, and the read-only scope-row lookup.
// SPORT: internal.retrieval recall_acceptance (ADD) (P1-CPA-09).
package retrieval_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" driver the read-only lookup uses.
)

// acceptSeed is testdata/acceptance/seed.json.
type acceptSeed struct {
	Query, Conversation, Memory, File, Restricted, LocalOnly string
	MemoryName                                               string `json:"memory_name"`
	VaultName                                                string `json:"vault_name"`
	VaultValue                                               string `json:"vault_value"`
	RestrictedQuery                                          string `json:"restricted_query"`
}

// loadSeed reads the committed fixture.
func loadSeed(t *testing.T) acceptSeed {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "acceptance", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s acceptSeed
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// whatEnvelope is the D/S-06.T5 versioned `recall what --json` shape.
type whatEnvelope struct {
	Version int  `json:"version"`
	OK      bool `json:"ok"`
	Data    struct {
		Query     string         `json:"query"`
		Results   []whatResult   `json:"results"`
		Citations []whatCitation `json:"citations"`
		Withheld  int            `json:"withheld"`
		Rendered  string         `json:"rendered"`
		Legs      []string       `json:"legs"`
	} `json:"data"`
}

type whatResult struct {
	Rank       int      `json:"rank"`
	Domain     string   `json:"domain"`
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	CorpusID   string   `json:"corpus_id"`
	Trust      string   `json:"trust"`
	Score      float64  `json:"score"`
	RawScore   float64  `json:"raw_score"`
	Strategies []string `json:"strategies"`
	Snippet    string   `json:"snippet"`
}

type whatCitation struct {
	ChunkID  string `json:"chunk_id"`
	Path     string `json:"path"`
	Lines    struct{ Start, End int }
	CorpusID string   `json:"corpus_id"`
	Trust    string   `json:"trust"`
	Rank     int      `json:"rank"`
	Score    float64  `json:"score"`
	RawScore float64  `json:"raw_score"`
	Strat    []string `json:"strategies"`
}

// decodeWhat decodes stdout strictly into the envelope.
func decodeWhat(t *testing.T, stdout string) whatEnvelope {
	t.Helper()
	var env whatEnvelope
	dec := json.NewDecoder(bytes.NewReader([]byte(stdout)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("recall what --json is not the versioned schema: %v\n%s", err, stdout)
	}
	return env
}

// seedThread creates a thread (privacy tier, cwd = the workspace) and
// appends one user turn, over the daemon socket. It returns the thread id.
func (e *acceptEnv) seedThread(t *testing.T, tier, text string) string {
	t.Helper()
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	e.rpc(t, "chat.create_thread", map[string]string{"privacy_mode": tier, "cwd": e.work}, &created)
	var appended struct {
		TurnID string `json:"turn_id"`
	}
	e.rpc(t, "chat.append_turn", map[string]any{
		"thread_id": created.ThreadID, "role": "user",
		"segments": []map[string]string{{"kind": "text", "content": text}},
	}, &appended)
	if created.ThreadID == "" || appended.TurnID == "" {
		t.Fatalf("seeding %s thread: empty ids %+v %+v", tier, created, appended)
	}
	return created.ThreadID
}

// threadIDs lists every thread id the daemon holds.
func (e *acceptEnv) threadIDs(t *testing.T) map[string]bool {
	t.Helper()
	var res struct {
		Threads []struct{ ID string }
	}
	e.rpc(t, "chat.list_threads", nil, &res)
	ids := map[string]bool{}
	for _, th := range res.Threads {
		ids[th.ID] = true
	}
	return ids
}

// storedTurn is one stored turn's segment texts.
type storedTurn struct{ Segments []struct{ Content string } }

// turns reads a thread's stored turns (the daemon's state, not an exit code).
func (e *acceptEnv) turns(t *testing.T, threadID string) []storedTurn {
	t.Helper()
	var res struct{ Turns []storedTurn }
	e.rpc(t, "chat.get_thread", map[string]string{"thread_id": threadID}, &res)
	return res.Turns
}

// totalTurns counts the stored turns across every thread.
func (e *acceptEnv) totalTurns(t *testing.T) int {
	t.Helper()
	n := 0
	for id := range e.threadIDs(t) {
		n += len(e.turns(t, id))
	}
	return n
}

// projectRef reads conversation_thread_project.project_ref through a
// read-only connection; found is false when no row exists.
func (e *acceptEnv) projectRef(t *testing.T, threadID string) (ref string, found bool) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.home, ".cascade", "data", "cascade.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	err = db.QueryRow(`SELECT project_ref FROM conversation_thread_project WHERE thread_id = ?`, threadID).Scan(&ref)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("reading the scope row: %v", err)
	}
	return ref, true
}
