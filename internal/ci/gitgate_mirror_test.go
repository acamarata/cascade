package ci

// Purpose: the mirror pusher's behaviour with injected git, queue, effect
// log and attention seams: off by default, fast-forward only, attestation
// journalled, crash recovery by probing the remote, failure never fatal.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	mirrorTip    = "0123456789abcdef0123456789abcdef01234567"
	mirrorOldTip = "89abcdef0123456789abcdef0123456789abcdef"
	mirrorRemote = "ssh://git@mirror.example.org/x.git"
)

type fakeMirrorGit struct {
	remoteTip string
	ancestor  bool
	pushErr   error
	pushes    [][]string
	probes    int
}

func (g *fakeMirrorGit) RemoteTip(context.Context, string, string) (string, error) {
	g.probes++
	return g.remoteTip, nil
}

func (g *fakeMirrorGit) IsAncestor(context.Context, string, string) (bool, error) {
	return g.ancestor, nil
}

func (g *fakeMirrorGit) Push(_ context.Context, remote, tip, branch string) error {
	g.pushes = append(g.pushes, MirrorPushArgv(remote, tip, branch))
	if g.pushErr == nil {
		g.remoteTip = tip
	}
	return g.pushErr
}

type fakeMirrorQueue struct{ jobs []MirrorJob }

func (q *fakeMirrorQueue) Enqueue(_ context.Context, j MirrorJob) error {
	q.jobs = append(q.jobs, j)
	return nil
}

// fakeEffects is an in-memory effect log: key -> phase, intents keep their request.
type fakeEffects struct {
	phase  map[string]string
	reqs   []audit.EffectRequest
	outcom []string
}

func newFakeEffects() *fakeEffects { return &fakeEffects{phase: map[string]string{}} }

func (e *fakeEffects) BeginEffect(_ context.Context, r audit.EffectRequest) (audit.EffectHandle, error) {
	if _, ok := e.phase[r.Key]; ok {
		return audit.EffectHandle{}, audit.ErrEffectExists
	}
	e.phase[r.Key] = "intent"
	e.reqs = append(e.reqs, r)
	return audit.EffectHandle{Key: r.Key, Kind: r.Kind}, nil
}

func (e *fakeEffects) ConfirmEffect(_ context.Context, h audit.EffectHandle, outcome string) (audit.Record, error) {
	e.phase[h.Key] = "confirmed"
	e.outcom = append(e.outcom, outcome)
	return audit.Record{}, nil
}

func (e *fakeEffects) FailEffect(_ context.Context, h audit.EffectHandle, _ string) (audit.Record, error) {
	e.phase[h.Key] = "failed"
	return audit.Record{}, nil
}

func (e *fakeEffects) pending(context.Context) ([]audit.EffectHandle, error) {
	var out []audit.EffectHandle
	for k, p := range e.phase {
		if p == "intent" {
			out = append(out, audit.EffectHandle{Key: k})
		}
	}
	return out, nil
}

type mirrorRig struct {
	p         *MirrorPusher
	git       *fakeMirrorGit
	queue     *fakeMirrorQueue
	effects   *fakeEffects
	attention []string
	records   []MirrorPush
}

func newMirrorRig(remote string, granted bool) *mirrorRig {
	r := &mirrorRig{git: &fakeMirrorGit{ancestor: true}, queue: &fakeMirrorQueue{}, effects: newFakeEffects()}
	r.p = &MirrorPusher{Remote: remote, Git: r.git, Queue: r.queue, Effects: r.effects, Pending: r.effects.pending,
		Granted:   func(context.Context) bool { return granted },
		Admit:     func(context.Context) error { return nil },
		Attention: func(_ context.Context, ref string) { r.attention = append(r.attention, ref) },
		Record:    func(m MirrorPush) { r.records = append(r.records, m) }}
	return r
}

func (r *mirrorRig) job() MirrorJob {
	return MirrorJob{Remote: mirrorRemote, Branch: "feature/x", Tip: mirrorTip, Attestation: "att-1"}
}

func TestMirrorPushDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]*mirrorRig{"remote unset": newMirrorRig("", true), "not granted": newMirrorRig(mirrorRemote, false)} {
		if err := r.p.Enqueue(ctx, "feature/x", mirrorTip, "att-1"); err != nil || len(r.queue.jobs) != 0 {
			t.Fatalf("%s: Enqueue = %v, queued %d; want a silent skip", name, err, len(r.queue.jobs))
		}
		if err := r.p.Run(ctx, r.job()); err != nil || r.git.probes != 0 || len(r.git.pushes) != 0 || len(r.effects.reqs) != 0 || len(r.attention) != 0 {
			t.Fatalf("%s: Run = %v, git touched %d/%d, effects %d, attention %d; want nothing", name, err, r.git.probes, len(r.git.pushes), len(r.effects.reqs), len(r.attention))
		}
	}
	on := newMirrorRig(mirrorRemote, true)
	if err := on.p.Enqueue(ctx, "feature/x", mirrorTip, "att-1"); err != nil || len(on.queue.jobs) != 1 {
		t.Fatalf("enabled Enqueue = %v, queued %d; want 1", err, len(on.queue.jobs))
	}
}

func TestMirrorPushFastForwardOnly(t *testing.T) {
	ctx := context.Background()
	r := newMirrorRig(mirrorRemote, true)
	r.git.remoteTip = mirrorOldTip
	if err := r.p.Run(ctx, r.job()); err != nil || len(r.git.pushes) != 1 {
		t.Fatalf("fast-forward Run = %v, pushes %d; want 1", err, len(r.git.pushes))
	}
	argv := strings.Join(r.git.pushes[0], " ")
	for _, bad := range []string{"--mirror", "--force", "-f ", "--tags", "refs/tags", "+", "--delete"} {
		if strings.Contains(argv, bad) {
			t.Fatalf("argv %q carries %q", argv, bad)
		}
	}
	if want := "git push --porcelain -- " + mirrorRemote + " " + mirrorTip + ":refs/heads/feature/x"; argv != want {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	nff := newMirrorRig(mirrorRemote, true)
	nff.git.remoteTip, nff.git.ancestor = mirrorOldTip, false
	if err := nff.p.Run(ctx, nff.job()); err != nil || len(nff.git.pushes) != 0 || len(nff.attention) != 1 {
		t.Fatalf("non-fast-forward Run = %v, pushes %d, attention %d; want refused before push", err, len(nff.git.pushes), len(nff.attention))
	}
}

func TestMirrorPushJournalsAttestation(t *testing.T) {
	r := newMirrorRig(mirrorRemote, true)
	if err := r.p.Run(context.Background(), r.job()); err != nil {
		t.Fatal(err)
	}
	key := MirrorEffectKey(r.job())
	if len(r.effects.reqs) != 1 || r.effects.reqs[0].Key != key || !strings.HasPrefix(key, MirrorEffectPrefix) {
		t.Fatalf("intent = %+v, want one intent keyed %q", r.effects.reqs, key)
	}
	if !strings.Contains(string(r.effects.reqs[0].Explain), "att-1") {
		t.Fatalf("intent explain %s does not name the attestation", r.effects.reqs[0].Explain)
	}
	if r.effects.phase[key] != "confirmed" || len(r.effects.outcom) != 1 || len(r.records) != 1 || !r.records[0].OK {
		t.Fatalf("phase %q, outcomes %v, records %+v; want confirmed and a good status record", r.effects.phase[key], r.effects.outcom, r.records)
	}
	if err := r.p.Run(context.Background(), MirrorJob{Remote: mirrorRemote, Branch: "x", Tip: mirrorTip}); err == nil {
		t.Fatal("a job with no attestation id ran")
	}
}

// TestMirrorPushEffectCrash: the process died after the push but before the
// confirm; recovery finds the open intent, sees the tip on the remote and
// confirms without a second push.
func TestMirrorPushEffectCrash(t *testing.T) {
	r := newMirrorRig(mirrorRemote, true)
	job := r.job()
	if _, err := r.effects.BeginEffect(context.Background(), audit.EffectRequest{Kind: audit.KindExternalEffect, Key: MirrorEffectKey(job)}); err != nil {
		t.Fatal(err)
	}
	r.git.remoteTip = mirrorTip
	if err := r.p.Run(context.Background(), job); err != nil {
		t.Fatalf("recovery Run: %v", err)
	}
	if len(r.git.pushes) != 0 || r.effects.phase[MirrorEffectKey(job)] != "confirmed" || r.effects.outcom[0] != "already-present" {
		t.Fatalf("pushes %d, phase %q, outcomes %v; want no push and a confirmed already-present", len(r.git.pushes), r.effects.phase[MirrorEffectKey(job)], r.effects.outcom)
	}
	if err := r.p.Run(context.Background(), job); err != nil || len(r.git.pushes) != 0 {
		t.Fatalf("second Run = %v with %d pushes; a finished key must not run again", err, len(r.git.pushes))
	}
}

func TestMirrorPushFailureNonFatal(t *testing.T) {
	r := newMirrorRig(mirrorRemote, true)
	r.git.pushErr = cascade.New(cascade.KindUnavailable, "remote down")
	if err := r.p.Run(context.Background(), r.job()); err != nil {
		t.Fatalf("a failed push returned %v; acceptance must stay", err)
	}
	key := MirrorEffectKey(r.job())
	if len(r.attention) != 1 || r.attention[0] != key || r.effects.phase[key] != "failed" {
		t.Fatalf("attention %v, phase %q; want one item and a failed effect", r.attention, r.effects.phase[key])
	}
	if len(r.records) != 1 || r.records[0].OK || !strings.Contains(r.records[0].Error, "remote down") {
		t.Fatalf("status record = %+v, want the failure", r.records)
	}
}

// TestMirrorPushArgvMatchesFixture pins the argv to the one captured from a
// real push, and the environment filter against a token-bearing parent.
func TestMirrorPushArgvMatchesFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/gitgate/mirror_push_fixture.txt")
	if err != nil {
		t.Fatal(err)
	}
	var want string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "argv: ") {
			want = strings.TrimPrefix(l, "argv: ")
		}
	}
	got := strings.Join(MirrorPushArgv("REMOTE", "TIP", "BRANCH"), " ")
	if got != want {
		t.Fatalf("argv %q, fixture %q", got, want)
	}
	env := strings.Join(MirrorPushEnv([]string{"PATH=/bin", "GITHUB_TOKEN=secret", "HOME=/h", "AWS_SECRET_ACCESS_KEY=x"}), " ")
	if strings.Contains(env, "secret") || strings.Contains(env, "AWS") || !strings.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("env %q leaks or lacks the prompt guard", env)
	}
}
