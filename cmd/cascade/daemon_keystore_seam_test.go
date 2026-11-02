//go:build !windows

package main

import (
	"context"
	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcceptanceKeystoreUsesSecretsDir(t *testing.T) {
	var configs []secrets.Config
	w, r := attestedDaemon(t, guardedAcceptanceKeystore(t, &configs))
	ref := attestationCheckpoint(t, w, r)
	if result := hookDispatch(t, w, ref.JobID); result.Deny {
		t.Fatalf("acceptance refused: %+v", result)
	}
	if len(configs) != 1 || !configs[0].ForceFileVault || configs[0].Dir != r.deps.SecretsDir {
		t.Fatalf("keystore configs: %+v", configs)
	}
}

type acceptanceClock struct{ seconds atomic.Int64 }

func (c *acceptanceClock) Now() time.Time { return time.Unix(c.seconds.Load(), 0) }

func acceptanceWaiter() (*ciAcceptanceVerifier, *acceptanceClock, *ciFakeTicker) {
	clock := &acceptanceClock{}
	tick := &ciFakeTicker{c: make(chan struct{}, 1)}
	v := &ciAcceptanceVerifier{clock: clock, terminal: make(chan struct{}, 1),
		ticker: func() runtime.Ticker { return tick }}
	return v, clock, tick
}

func TestCIAcceptanceVerifierWaitsForTerminal(t *testing.T) {
	t.Run("all kinds", acceptanceWaitsForAll)
	t.Run("injected deadline", acceptanceTimesOut)
	t.Run("composed missing runtime", acceptanceMissingCI)
	t.Run("missing runtime", func(t *testing.T) {
		_, err := newCIAcceptanceVerifier(&daemonWiring{})
		if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
			t.Fatalf("missing runtime error = %v", err)
		}
	})
}

func acceptanceMissingCI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	addTestRegistration(t, daemonRegistration{Name: "test-remove-ci", Phase: phaseSupervised, Order: 22,
		Wire: func(w *daemonWiring) error { w.Deps.CI = nil; return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	_, cleanups, err := composeDaemon(ctx, newRunTestDeps(t, nil), nil)
	cancel()
	runCleanupsLIFO(cleanups)
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "completion-gate") {
		t.Fatalf("missing CI composition = %v", err)
	}
}

func acceptanceWaitsForAll(t *testing.T) {
	v, _, _ := acceptanceWaiter()
	v.terminal = make(chan struct{})
	started, finish := make(chan struct{}), make(chan struct{})
	result := make(chan jobs.AcceptanceVerdict, 1)
	go func() {
		verdict, err := v.wait(context.Background(), func(context.Context) (ci.AcceptanceResult, error) {
			close(started)
			<-finish
			return ci.AcceptanceResult{Passed: true, CheckpointID: "checkpoint", TreeHash: "tree"}, nil
		})
		if err != nil {
			verdict.Passed = false
		}
		result <- verdict
	}()
	<-started
	for range 2 {
		select {
		case v.terminal <- struct{}{}:
		case got := <-result:
			t.Fatalf("returned on an intermediate terminal: %+v", got)
		case <-time.After(testDaemonBound):
			t.Fatal("terminal callback did not wake verification")
		}
	}
	select {
	case got := <-result:
		t.Fatalf("returned before every kind finished: %+v", got)
	default:
	}
	close(finish)
	select {
	case got := <-result:
		if !got.Passed || got.CheckpointID != "checkpoint" || got.TreeHash != "tree" {
			t.Fatalf("terminal verdict = %+v", got)
		}
	case <-time.After(testDaemonBound):
		t.Fatal("all terminal kinds did not finish verification")
	}
}

func acceptanceTimesOut(t *testing.T) {
	v, clock, tick := acceptanceWaiter()
	started, canceled := make(chan struct{}), make(chan struct{})
	result := make(chan jobs.AcceptanceVerdict, 1)
	go func() {
		verdict, _ := v.wait(context.Background(), func(ctx context.Context) (ci.AcceptanceResult, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return ci.AcceptanceResult{}, ctx.Err()
		})
		result <- verdict
	}()
	<-started
	clock.seconds.Store(int64((30 * time.Minute) / time.Second))
	tick.c <- struct{}{}
	select {
	case got := <-result:
		if got.Passed || got.Reason != "acceptance timed out" {
			t.Fatalf("deadline verdict = %+v", got)
		}
	case <-time.After(testDaemonBound):
		t.Fatal("injected-clock bound did not end verification")
	}
	<-canceled
}
