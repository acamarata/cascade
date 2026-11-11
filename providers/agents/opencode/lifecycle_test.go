package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestOpenCodeOutputFilterErrorWithholds(t *testing.T) {
	for _, stderr := range []bool{false, true} {
		t.Run(map[bool]string{false: "stdout", true: "stderr"}[stderr], func(t *testing.T) {
			k := newKit(t)
			if stderr {
				k.opts.Stderr = "withhold\n"
			}
			s := k.spec()
			s.Filter = func(b []byte) ([]byte, error) {
				if !stderr || strings.Contains(string(b), "withhold") {
					return nil, ErrStart
				}
				return b, nil
			}
			r, c, err := k.run(s)
			wantError(t, err, provider.ErrEgressSubstitutionFailed)
			if c.Output != "" {
				t.Fatal("failed output leaked")
			}
			e, _ := k.d.Events(r.JobID)
			for _, v := range e {
				if strings.Contains(string(v.Native.Raw), "withhold") {
					t.Fatal("native leak")
				}
			}
		})
	}
}

func TestOpenCodeErrorPathsTyped(t *testing.T) {
	for _, tc := range []struct {
		name, lines string
		exit        int
		want        error
	}{
		{"malformed", "{\n", 0, ErrMalformedEvent},
		{"unknown", "{\"type\":\"future\"}\n", 0, ErrUnknownEventKind},
		{"nonzero", "", 3, ErrNonZeroExit},
		{"missing-terminal", "", 0, ErrMissingTerminal},
		{"oversize", strings.Repeat("x", maxEventBytes+1), 0, ErrMalformedEvent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newKit(t)
			k.opts.Lines = tc.lines
			k.opts.Exit = tc.exit
			_, _, err := k.run(k.spec())
			wantError(t, err, tc.want)
		})
	}
	t.Run("notfound", func(t *testing.T) {
		k := newKit(t)
		k.cfg.Binary = filepath.Join(k.root, "absent")
		_, err := k.driver().Spawn(context.Background(), k.spec())
		wantError(t, err, ErrDriverNotFound)
	})
	t.Run("start", func(t *testing.T) {
		k := newKit(t)
		k.cfg.Binary = filepath.Join(k.root, "file")
		if err := os.WriteFile(k.cfg.Binary, []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := k.driver().Spawn(context.Background(), k.spec())
		wantError(t, err, ErrStart)
	})
	t.Run("cancel", TestOpenCodeCancelConfirmsExit)
}

func TestOpenCodeCancelConfirmsExit(t *testing.T) {
	k := newKit(t)
	k.opts.Lines = ""
	k.opts.Block = true
	d := k.driver()
	r, err := d.Spawn(context.Background(), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Cancel(context.Background(), r.JobID); err != nil {
		t.Fatal(err)
	}
	s, err := d.Status(context.Background(), r.JobID)
	if err != nil || s != provider.AgentRunCancelled {
		t.Fatal(s, err)
	}
	_, err = d.Collect(context.Background(), r.JobID)
	wantError(t, err, ErrRunCancelled)
	if !procgroup.Empty(r.ProcessGroupID) {
		t.Fatal("cancel returned before group exit")
	}
}

func TestOpenCodeUsesSpecJobID(t *testing.T) {
	k := newKit(t)
	s := k.spec()
	s.JobID = "requested"
	r, c, err := k.run(s)
	if err != nil || r.JobID != s.JobID || c.JobID != s.JobID {
		t.Fatal(r, c, err)
	}
}

func TestOpenCodeExecutableReported(t *testing.T) {
	k := newKit(t)
	if k.driver().Executable() != k.cfg.Binary {
		t.Fatal("wrong executable")
	}
}

func TestOpenCodeMessageUnsupported(t *testing.T) {
	k := newKit(t)
	r, _, err := k.run(k.spec())
	if err != nil {
		t.Fatal(err)
	}
	wantError(t, k.d.Message(context.Background(), r.JobID, "turn"), provider.ErrMessageUnsupported)
	wantError(t, k.d.Message(context.Background(), "absent", "turn"), provider.ErrJobNotFound)
}

func TestOpenCodeReadMethodsAndGuards(t *testing.T) {
	k := newKit(t)
	d := k.driver()
	ctx := context.Background()
	_, err := d.Status(ctx, "missing")
	wantError(t, err, provider.ErrJobNotFound)
	_, err = d.Collect(ctx, "missing")
	wantError(t, err, provider.ErrJobNotFound)
	_, err = d.Artifacts(ctx, "missing")
	wantError(t, err, provider.ErrJobNotFound)
	_, err = d.Events("missing")
	wantError(t, err, provider.ErrJobNotFound)
	wantError(t, d.Cancel(ctx, "missing"), provider.ErrJobNotFound)
	_, err = d.Chat(ctx, provider.ChatRequest{})
	wantError(t, err, errUnsupported)
	_, err = d.Embed(ctx, provider.ModelEmbedRequest{})
	wantError(t, err, errUnsupported)
	_, err = d.Count(ctx, provider.CountRequest{})
	wantError(t, err, errUnsupported)
	wantError(t, d.Stream(ctx, provider.ChatRequest{}, nil), errUnsupported)
	wantError(t, d.ResolveApproval("", ""), errUnsupported)
	for _, cfg := range []Config{{}, {Binary: "bin", VendorAuthVar: "HOME"}, {Binary: "bin", Defaults: provider.DriverDefaults{Effort: "bad"}}} {
		_, err := New(cfg)
		wantError(t, err, ErrInvalidConfig)
	}
	s := k.spec()
	s.Effort = "bad"
	_, err = d.Spawn(ctx, s)
	wantError(t, err, ErrInvalidConfig)
	s = k.spec()
	s.ToolPolicy = "bad"
	_, err = d.Spawn(ctx, s)
	wantError(t, err, provider.ErrInvalidToolPolicy)
	s = k.spec()
	s.Env.Home = "other"
	_, err = d.Spawn(ctx, s)
	wantError(t, err, ErrHomeMismatch)
	d.cfg.Clock = nil
	_, err = d.Spawn(ctx, k.spec())
	wantError(t, err, ErrNoClock)
}
