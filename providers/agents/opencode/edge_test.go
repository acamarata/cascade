package opencode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestOpenCodeEnvironmentEdges(t *testing.T) {
	k := newKit(t)
	d := k.driver()
	for _, extra := range []string{"HOME=duplicate", "malformed"} {
		e := k.env
		e.Driver = append(append([]string{}, e.Driver...), extra)
		wantError(t, d.checkEnv(e), ErrEnvNotAllowed)
	}
	e := k.env
	e.Driver = []string{"HOME=" + k.home, VendorAuthVar + "=relative", KeyVarCompatKey + "=fixture"}
	wantError(t, d.checkEnv(e), ErrHomeMismatch)
	e.Driver = []string{"HOME=" + k.home, VendorAuthVar + "=" + t.TempDir(), KeyVarCompatKey + "=fixture"}
	wantError(t, d.checkEnv(e), ErrHomeMismatch)
	if profileContains("relative", k.home) || profileContains(k.home, filepath.Join(k.home, "absent")) || profileContains(filepath.Join(k.home, "absent"), k.home) {
		t.Fatal("invalid profile accepted")
	}
	s := k.spec()
	s.Model = "--share"
	_, err := d.Spawn(context.Background(), s)
	wantError(t, err, ErrInvalidConfig)
	s = k.spec()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = d.Spawn(ctx, s)
	wantError(t, err, ErrRunCancelled)
	d.cfg.Binary = "relative"
	_, err = d.Spawn(context.Background(), s)
	wantError(t, err, ErrDriverNotFound)
	_, _, err = decodeEvent([]byte(`{"type":"step_finish","part":{"reason":"tool-calls"}}`), 1, provider.DataClassInternal)
	wantError(t, err, ErrMalformedEvent)
}

func TestOpenCodeUnconfirmedGroupRefuses(t *testing.T) {
	k := newKit(t)
	d := k.driver()
	// Group zero is never a valid child group and must not be signalled or
	// mistaken for confirmation of exit.
	if d.confirmGroup(&job{pgid: 0}) {
		t.Fatal("confirmed invalid group")
	}
	j := &job{id: "unconfirmed", done: make(chan struct{}), state: provider.AgentRunRunning}
	d.finish(j, nil, nil, false, false)
	d.jobs[j.id] = j
	wantError(t, d.Cancel(context.Background(), j.id), provider.ErrCancelUnconfirmed)
	delete(d.jobs, j.id)
	j = &job{id: "waiting", done: make(chan struct{}), state: provider.AgentRunCancelling, stopping: true}
	d.jobs[j.id] = j
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wantError(t, d.Cancel(ctx, j.id), provider.ErrCancelUnconfirmed)
	_, err := d.Collect(ctx, j.id)
	wantError(t, err, ErrRunCancelled)
	delete(d.jobs, j.id)
}

func TestOpenCodeDuplicateAndDefaults(t *testing.T) {
	k := newKit(t)
	d := k.driver()
	s := k.spec()
	s.JobID = "duplicate"
	r, err := d.Spawn(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Spawn(context.Background(), s)
	wantError(t, err, ErrDuplicateJob)
	if _, err = d.Collect(context.Background(), r.JobID); err != nil {
		t.Fatal(err)
	}
	d.cfg.Defaults.Effort = "low"
	args := d.argv(s)
	if args[len(args)-4] != "--variant" || args[len(args)-3] != "low" {
		t.Fatal(args)
	}
	cfg := Config{Binary: k.cfg.Binary}
	fresh, err := New(cfg)
	if err != nil || fresh.cfg.Defaults.CancelGrace != 30*time.Second {
		t.Fatal(err)
	}
}

func TestOpenCodeResidualPipeDoesNotHang(t *testing.T) {
	k := newKit(t)
	k.opts.Descendant = true
	d := k.driver()
	r, err := d.Spawn(context.Background(), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	_, err = d.Collect(ctx, r.JobID)
	if err == ErrRunCancelled {
		t.Fatal("leader exit left Collect blocked on descendant pipes")
	}
	if err == provider.ErrCancelUnconfirmed {
		// An unreaped orphan can keep the group nonempty. The lease remains
		// cancelling and the refusal must not masquerade as a confirmed exit.
		state, _ := d.Status(ctx, r.JobID)
		if state != provider.AgentRunCancelling {
			t.Fatal(state)
		}
		d.mu.Lock()
		delete(d.jobs, r.JobID)
		d.mu.Unlock()
	} else if err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeDrainTimeoutWithholds(t *testing.T) {
	k := newKit(t)
	d := k.driver()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	er, ew, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = er.Close() }()
	defer func() { _ = ew.Close() }()
	j := &job{stdout: r, stderr: er}
	err, _ = d.drain(j, make(chan error), make(chan error))
	wantError(t, err, ErrMalformedEvent)
	j.closed = true
	wantError(t, j.record([]byte(`{"type":"text"}`)), ErrRunCancelled)
}
