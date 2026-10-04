// Purpose: the crash window of contract:fanout-leg-results (C16 crash
//   injection): a child process is SIGKILLed between PutLegResult and the
//   done append, and the next run replays the leg with zero provider calls;
//   plus the replay refusals and returned journal/store errors.
// Constraints: the child is this test binary re-executed; READY on stdout
//   is the only synchronization; durable doubles write under t.TempDir.
// SPORT: conductor.fanout/CHANGE (tests) (P1-CORE-18).

package conductor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const crashHelperEnv = "CASCADE_FANOUT_CRASH_DIR"

// dirLegStore is a durable LegResultStore double: one O_EXCL file per key.
type dirLegStore struct{ dir string }

func (s dirLegStore) PutLegResult(_ context.Context, r LegResult) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, LegResultKey(r.FanOutID, r.LegIndex)+".rec"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return cascade.Wrap(cascade.KindConflict, err, "dirLegStore: create-only")
	}
	_, werr := f.Write(b)
	return errors.Join(werr, f.Sync(), f.Close())
}

func (s dirLegStore) GetLegResult(_ context.Context, fanoutID string, legIndex int) (LegResult, bool, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, LegResultKey(fanoutID, legIndex)+".rec"))
	if errors.Is(err, os.ErrNotExist) {
		return LegResult{}, false, nil
	}
	if err != nil {
		return LegResult{}, false, err
	}
	var r LegResult
	return r, true, json.Unmarshal(b, &r)
}

func (s dirLegStore) DeleteTask(context.Context, string) error { return nil }

// fileJournal is a durable JournalAppender double: one JSON file per
// entry. hook runs before an append (the crash-injection point), after
// once it has landed.
type fileJournal struct {
	dir         string
	mu          sync.Mutex
	hook, after func(kind string, leg int)
}

type fileEntry struct {
	Kind   string            `json:"kind"`
	Leg    int               `json:"leg"`
	Fields map[string]string `json:"fields"`
}

func (j *fileJournal) AppendLeg(_ context.Context, kind, _ string, leg int, fields map[string]string) (uint64, error) {
	if j.hook != nil {
		j.hook(kind, leg)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	entries := j.read()
	attempt := uint64(1)
	for _, e := range entries {
		if kind == legKindStarted && e.Kind == legKindStarted && e.Leg == leg {
			attempt++
		}
	}
	if kind == legKindDone {
		attempt, _ = strconv.ParseUint(fields["attempt"], 10, 64)
	}
	b, _ := json.Marshal(fileEntry{Kind: kind, Leg: leg, Fields: fields})
	err := os.WriteFile(filepath.Join(j.dir, "j-"+strconv.Itoa(1000+len(entries))+".log"), b, 0o600)
	if j.after != nil {
		j.after(kind, leg)
	}
	return attempt, err
}

func (j *fileJournal) read() []fileEntry {
	paths, _ := filepath.Glob(filepath.Join(j.dir, "j-*.log"))
	out := make([]fileEntry, 0, len(paths))
	for _, p := range paths {
		var e fileEntry
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func (j *fileJournal) completed() map[int]JobID {
	out := map[int]JobID{}
	for _, e := range j.read() {
		if e.Kind == legKindDone && e.Fields["outcome"] == LegOutcomeOK {
			out[e.Leg] = JobID(e.Fields["job_id"])
		}
	}
	return out
}

// TestFanOutCrashHelperProcess is the child TestFanOutCrashBetweenPutAndDone
// kills. Run directly (env unset) it is a no-op.
func TestFanOutCrashHelperProcess(_ *testing.T) {
	dir := os.Getenv(crashHelperEnv)
	if dir == "" {
		return
	}
	leg0Done := make(chan struct{})
	var once sync.Once
	j := &fileJournal{dir: dir}
	j.hook = func(kind string, leg int) {
		if kind == legKindDone && leg == 1 { // leg 1's record is stored; its done never lands
			<-leg0Done
			_, _ = os.Stdout.WriteString("READY\n")
			select {}
		}
	}
	j.after = func(kind string, leg int) {
		if kind == legKindDone && leg == 0 {
			once.Do(func() { close(leg0Done) })
		}
	}
	exec, _ := outputExec("child")
	_, _ = FanOut(context.Background(), "fo-crash", fanoutReq(), 2, nil, passthroughPermit, j, dirLegStore{dir}, allowAll, exec)
}

func TestFanOutCrashBetweenPutAndDone(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFanOutCrashHelperProcess$")
	cmd.Env = append(os.Environ(), crashHelperEnv+"="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if line != "READY\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child: want READY, got %q", line)
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL between PutLegResult and the done append
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait()

	j, store := &fileJournal{dir: dir}, dirLegStore{dir}
	completed := j.completed()
	stored, found, _ := store.GetLegResult(context.Background(), "fo-crash", 1)
	if _, done := completed[1]; done || !found || len(completed) != 1 {
		t.Fatalf("crash state: completed=%v leg1 record=%v, want leg 0 done and leg 1 stored without done", completed, found)
	}
	exec, calls := outputExec("resume")
	var auth int32
	authorize := func(context.Context, provider.ModelRequest) error { atomic.AddInt32(&auth, 1); return nil }
	out, err := FanOut(context.Background(), "fo-crash", fanoutReq(), 2, completed, passthroughPermit, j, store, authorize, exec)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if *calls != 0 || auth != 2 {
		t.Fatalf("resume: provider calls = %d (want 0), authorize calls = %d (want 2)", *calls, auth)
	}
	if out[1].Output != stored.Response.Output || out[1].Output == "" {
		t.Fatalf("leg 1 = %q, want the stored %q", out[1].Output, stored.Response.Output)
	}
	if after := j.completed(); len(after) != 2 {
		t.Fatalf("completed after resume = %v, want both legs (missing done appended)", after)
	}
}

func TestFanOutReplayMissingResultRefuses(t *testing.T) {
	exec, calls := outputExec("x")
	var auth int32
	authorize := func(context.Context, provider.ModelRequest) error { atomic.AddInt32(&auth, 1); return nil }
	out, err := FanOut(context.Background(), "fo-m", fanoutReq(), 1, map[int]JobID{0: "job-0"}, passthroughPermit, &spyJournal{}, newMemLegStore(), authorize, exec)
	if err != ErrLegResultMissing || !cascade.HasKind(err, cascade.KindNotFound) || err.Error() != ErrLegResultMissing.Error() {
		t.Fatalf("err = %v, want ErrLegResultMissing (KindNotFound)", err)
	}
	if out != nil || *calls != 0 || auth != 0 {
		t.Fatalf("refusal leaked: out=%v exec=%d authorize=%d, want nil/0/0", out, *calls, auth)
	}
}

func TestFanOutReplayDigestMismatchRefuses(t *testing.T) {
	other := fanoutReq()
	other.Inputs = []provider.ChatMessage{{Role: "user", Content: "a different prompt"}}
	for _, tc := range []struct {
		name      string
		completed map[int]JobID
		seedID    string
		seedReq   provider.ModelRequest
	}{
		{"completed, other prompt", map[int]JobID{0: "job-0"}, "fo-d", other},
		{"crash window, other prompt", nil, "fo-d", other},
		{"record copied from another fan-out", nil, "fo-other", fanoutReq()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemLegStore()
			seedLegRecord(t, store, tc.seedID, tc.seedReq, 0, provider.ModelResponse{JobID: "job-0", Output: "stored"})
			if tc.seedID != "fo-d" { // plant the foreign record under fo-d's key
				store.recs["fo-d#0"] = store.recs["fo-other#0"]
			}
			before := store.snapshot()
			exec, calls := outputExec("x")
			out, err := FanOut(context.Background(), "fo-d", fanoutReq(), 1, tc.completed, passthroughPermit, &spyJournal{}, store, allowAll, exec)
			if err != ErrLegResultMismatch || !cascade.HasKind(err, cascade.KindConflict) || err.Error() != ErrLegResultMismatch.Error() {
				t.Fatalf("err = %v, want ErrLegResultMismatch (KindConflict)", err)
			}
			if out != nil || *calls != 0 || store.snapshot()["fo-d#0"] != before["fo-d#0"] {
				t.Fatalf("mismatch leaked or dispatched: out=%v exec=%d", out, *calls)
			}
		})
	}
}

// holds reports whether target itself (pointer identity, not Kind) is in
// err's wrap/join tree.
func holds(err, target error) bool {
	if err == target {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if holds(e, target) {
				return true
			}
		}
		return false
	}
	if u := errors.Unwrap(err); u != nil {
		return holds(u, target)
	}
	return false
}

func TestFanOutJournalErrorIsReturned(t *testing.T) {
	boom, execErr := cascade.New(cascade.KindUnavailable, "journal down"), cascade.New(cascade.KindUnavailable, "provider down")
	for _, tc := range []struct {
		name, failOn    string
		putErr, getErr  error
		execErr         error
		seeded, wantRec bool
		wantExec        int32
	}{
		{name: "started append", failOn: legKindStarted},
		{name: "done append after put", failOn: legKindDone, wantRec: true, wantExec: 1},
		{name: "put", putErr: boom, wantExec: 1},
		{name: "get", getErr: boom},
		{name: "done append after a failed leg", failOn: legKindDone, execErr: execErr, wantExec: 1},
		{name: "replay done append", failOn: legKindDone, seeded: true, wantRec: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, j := newMemLegStore(), &spyJournal{failOn: tc.failOn, failErr: boom}
			if tc.seeded {
				seedLegRecord(t, store, "fo-j", fanoutReq(), 0, provider.ModelResponse{JobID: "job-0", Output: "stored"})
			}
			store.putErr, store.getErr = tc.putErr, tc.getErr
			var calls int32
			exec := func(context.Context, provider.ModelRequest) (provider.ModelResponse, error) {
				atomic.AddInt32(&calls, 1)
				return provider.ModelResponse{JobID: "job-0", Output: "fresh"}, tc.execErr
			}
			out, err := FanOut(context.Background(), "fo-j", fanoutReq(), 1, nil, passthroughPermit, j, store, allowAll, exec)
			if !holds(err, boom) || (tc.execErr != nil && !holds(err, tc.execErr)) || out != nil {
				t.Fatalf("err = %v out = %v, want the store/journal error returned (and the leg error kept)", err, out)
			}
			_, found, _ := (&memLegStore{recs: store.recs}).GetLegResult(context.Background(), "fo-j", 0)
			if calls != tc.wantExec || found != tc.wantRec || len(j.completedOK("fo-j")) != 0 {
				t.Fatalf("exec=%d record=%v completed=%v, want exec=%d record=%v and no ok done", calls, found, j.completedOK("fo-j"), tc.wantExec, tc.wantRec)
			}
		})
	}
}
