package swarm

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type mockBackend struct {
	resp Response
	err  error
	req  Request
}

func (m *mockBackend) Execute(req Request) (Response, error) {
	m.req = req
	return m.resp, m.err
}

// typedNilBackend is used to test typed-nil clearing.
type typedNilBackend struct{}

func (t *typedNilBackend) Execute(req Request) (Response, error) {
	return Response{}, nil
}

func TestReadySubcommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		setup    func() Backend
		out      *bytes.Buffer
		errw     *bytes.Buffer
		wantCode int
		wantOut  string
		wantErr  string
		checkReq bool
		wantReq  Request
	}{
		{
			name:     "success ready command",
			args:     []string{"ready", "mytarget"},
			setup:    func() Backend { return &mockBackend{resp: Response{Text: "ok"}} },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 0,
			wantOut:  "ok",
			wantErr:  "",
			checkReq: true,
			wantReq:  Request{Command: "ready", Target: "mytarget", SuggestSections: false},
		},
		{
			name:     "success with suggest-sections flag",
			args:     []string{"gen", "file.txt", "--suggest-sections"},
			setup:    func() Backend { return &mockBackend{resp: Response{Text: "done"}} },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 0,
			wantOut:  "done",
			wantErr:  "",
			checkReq: true,
			wantReq:  Request{Command: "gen", Target: "file.txt", SuggestSections: true},
		},
		{
			name:     "backend error",
			args:     []string{"ready", "mytarget"},
			setup:    func() Backend { return &mockBackend{err: errors.New("something broke")} },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 1,
			wantOut:  "",
			wantErr:  "pbd: something broke\n",
		},
		{
			name:     ErrBackendUnavailable.Error(),
			args:     []string{"ready", "mytarget"},
			setup:    func() Backend { return nil },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 1,
			wantOut:  "",
			wantErr:  "pbd: " + ErrBackendUnavailable.Error() + "\n",
		},
		{
			name:     "nil clearing",
			args:     []string{"ready", "mytarget"},
			setup:    func() Backend { ConfigureBackend(nil); return nil },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 1,
			wantOut:  "",
			wantErr:  "pbd: " + ErrBackendUnavailable.Error() + "\n",
		},
		{
			name:     "typed-nil clearing",
			args:     []string{"ready", "mytarget"},
			setup:    func() Backend { var b *typedNilBackend; ConfigureBackend(b); return nil },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 1,
			wantOut:  "",
			wantErr:  "pbd: " + ErrBackendUnavailable.Error() + "\n",
		},
		{
			name:     "nil out writer",
			args:     []string{"ready", "mytarget"},
			setup:    func() Backend { return &mockBackend{} },
			out:      nil,
			errw:     &bytes.Buffer{},
			wantCode: 1,
			wantOut:  "",
			wantErr:  "pbd: " + ErrInvalidWriter.Error() + "\n",
		},
		{
			name:     "nil out and nil errw",
			args:     []string{"ready", "mytarget"},
			setup:    func() Backend { return &mockBackend{} },
			out:      nil,
			errw:     nil,
			wantCode: 1,
			wantOut:  "",
			wantErr:  "",
		},
		{
			name:     "unknown command",
			args:     []string{"unknown", "mytarget"},
			setup:    func() Backend { return &mockBackend{} },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "usage: capp pbd {gen|ready|pack|fix|layout|suggest-sections} TARGET [--suggest-sections]\n",
		},
		{
			name:     "missing args",
			args:     []string{},
			setup:    func() Backend { return &mockBackend{} },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "usage: capp pbd {gen|ready|pack|fix|layout|suggest-sections} TARGET [--suggest-sections]\n",
		},
		{
			name:     "extra args",
			args:     []string{"ready", "mytarget", "--suggest-sections", "extra"},
			setup:    func() Backend { return &mockBackend{} },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "usage: capp pbd {gen|ready|pack|fix|layout|suggest-sections} TARGET [--suggest-sections]\n",
		},
		{
			name:     "empty target",
			args:     []string{"ready", "  "},
			setup:    func() Backend { return &mockBackend{} },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "usage: capp pbd {gen|ready|pack|fix|layout|suggest-sections} TARGET [--suggest-sections]\n",
		},
		{
			name:     "target starts with dash",
			args:     []string{"ready", "-foo"},
			setup:    func() Backend { return &mockBackend{} },
			out:      &bytes.Buffer{},
			errw:     &bytes.Buffer{},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "usage: capp pbd {gen|ready|pack|fix|layout|suggest-sections} TARGET [--suggest-sections]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.setup()
			ConfigureBackend(b)

			var outW io.Writer
			if tt.out != nil {
				outW = tt.out
			}
			var errW io.Writer
			if tt.errw != nil {
				errW = tt.errw
			}

			code := Run(tt.args, outW, errW)

			if code != tt.wantCode {
				t.Errorf("Run() code = %d, want %d", code, tt.wantCode)
			}
			if tt.out != nil && tt.out.String() != tt.wantOut {
				t.Errorf("Run() out = %q, want %q", tt.out.String(), tt.wantOut)
			}
			if tt.errw != nil && tt.errw.String() != tt.wantErr {
				t.Errorf("Run() errw = %q, want %q", tt.errw.String(), tt.wantErr)
			}
			if tt.checkReq {
				mb := b.(*mockBackend)
				if mb.req != tt.wantReq {
					t.Errorf("Run() request = %+v, want %+v", mb.req, tt.wantReq)
				}
			}
		})
	}
}
