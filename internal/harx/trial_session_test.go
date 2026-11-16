package harx

import (
	"context"
	"errors"
	"testing"
)

type fakeRunner struct {
	calls int
	last  SandboxRequest
	err   error
}

func (f *fakeRunner) Run(ctx context.Context, req SandboxRequest) (TrialSession, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return TrialSession{}, f.err
	}
	return TrialSession{SessionDir: "ignored", DeleteAfter: false}, nil
}

// TestTrialIsThrowawaySession verifies LaunchTrial behaviour.
func TestTrialIsThrowawaySession(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(nil, f, TrialRequest{Lane: "a", SessionDir: "s"})
		if !errors.Is(err, ErrInvalidTrial) {
			t.Fatalf("want ErrInvalidTrial, got %v", err)
		}
		if f.calls != 0 {
			t.Fatal("expected zero calls")
		}
	})

	t.Run("nil runner", func(t *testing.T) {
		_, err := LaunchTrial(context.Background(), nil, TrialRequest{Lane: "a", SessionDir: "s"})
		if !errors.Is(err, ErrInvalidTrial) {
			t.Fatalf("want ErrInvalidTrial, got %v", err)
		}
	})

	t.Run("typed nil runner", func(t *testing.T) {
		var r *fakeRunner
		_, err := LaunchTrial(context.Background(), r, TrialRequest{Lane: "a", SessionDir: "s"})
		if !errors.Is(err, ErrInvalidTrial) {
			t.Fatalf("want ErrInvalidTrial, got %v", err)
		}
	})

	t.Run("invalid lane empty", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{Lane: "", SessionDir: "s"})
		if !errors.Is(err, ErrInvalidTrial) || f.calls != 0 {
			t.Fatalf("bad: err=%v calls=%d", err, f.calls)
		}
	})

	t.Run("invalid lane chars", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{Lane: "a b", SessionDir: "s"})
		if !errors.Is(err, ErrInvalidTrial) || f.calls != 0 {
			t.Fatalf("bad: err=%v calls=%d", err, f.calls)
		}
	})

	t.Run("invalid session dir absolute", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{Lane: "a", SessionDir: "/abs"})
		if !errors.Is(err, ErrInvalidTrial) || f.calls != 0 {
			t.Fatalf("bad: err=%v calls=%d", err, f.calls)
		}
	})

	t.Run("invalid session dir dot", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{Lane: "a", SessionDir: "."})
		if !errors.Is(err, ErrInvalidTrial) || f.calls != 0 {
			t.Fatalf("bad: err=%v calls=%d", err, f.calls)
		}
	})

	t.Run("invalid file path", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{
			Lane: "a", SessionDir: "s",
			Files: []TrialFile{{Path: "/bad"}},
		})
		if !errors.Is(err, ErrInvalidTrial) || f.calls != 0 {
			t.Fatalf("bad: err=%v calls=%d", err, f.calls)
		}
	})

	t.Run("duplicate files", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{
			Lane: "a", SessionDir: "s",
			Files: []TrialFile{{Path: "x"}, {Path: "x"}},
		})
		if !errors.Is(err, ErrInvalidTrial) || f.calls != 0 {
			t.Fatalf("bad: err=%v calls=%d", err, f.calls)
		}
	})

	t.Run("success", func(t *testing.T) {
		f := &fakeRunner{}
		sess, err := LaunchTrial(context.Background(), f, TrialRequest{
			Lane:       "valid-lane_1",
			SessionDir: "my/session",
			Prompt:     "",
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if f.calls != 1 {
			t.Fatalf("expected 1 call, got %d", f.calls)
		}
		if !f.last.Sandbox {
			t.Fatal("expected Sandbox=true")
		}
		if !f.last.DeleteAfter {
			t.Fatal("expected DeleteAfter=true")
		}
		if f.last.SessionDir != "my/session" {
			t.Fatalf("expected clean relative dir, got %q", f.last.SessionDir)
		}
		if sess.SessionDir != "my/session" {
			t.Fatalf("expected session dir my/session, got %q", sess.SessionDir)
		}
		if !sess.DeleteAfter {
			t.Fatal("expected DeleteAfter=true on result")
		}
	})

	t.Run("runner error propagates", func(t *testing.T) {
		f := &fakeRunner{err: errors.New("boom")}
		sess, err := LaunchTrial(context.Background(), f, TrialRequest{Lane: "a", SessionDir: "s"})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("expected boom, got %v", err)
		}
		if sess != (TrialSession{}) {
			t.Fatal("expected zero session")
		}
	})

	t.Run("nil files and env valid", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{
			Lane: "a", SessionDir: "s", Files: nil, Env: nil,
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("invalid checker path absolute", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{
			Lane: "a", SessionDir: "s", CheckerPath: "/abs/checker",
		})
		if !errors.Is(err, ErrInvalidTrial) || f.calls != 0 {
			t.Fatalf("bad: err=%v calls=%d", err, f.calls)
		}
	})

	t.Run("invalid checker path dotdot", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{
			Lane: "a", SessionDir: "s", CheckerPath: "../checker",
		})
		if !errors.Is(err, ErrInvalidTrial) || f.calls != 0 {
			t.Fatalf("bad: err=%v calls=%d", err, f.calls)
		}
	})

	t.Run("empty checker source and output valid", func(t *testing.T) {
		f := &fakeRunner{}
		_, err := LaunchTrial(context.Background(), f, TrialRequest{
			Lane: "a", SessionDir: "s", CheckerSource: "", CheckerOutput: "",
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}
