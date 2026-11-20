package summaryrewrite

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

var errFake = errors.New("fake error")

func TestRewriteSummaryErrorPaths(t *testing.T) {
	goodRewriter := &fakeRewriter{
		local: true,
		rewrite: func(ctx context.Context, summary string) (string, error) {
			return "rewritten", nil
		},
	}
	goodStore := &fakeStore{}
	goodSession := Session{ID: "id", Summary: "summary", Private: false}

	tests := []struct {
		name    string
		ctx     context.Context
		session Session
		m       Rewriter
		store   Store
		wantErr error
	}{
		{
			name:    "nil context returns ErrInvalidRewrite",
			ctx:     nil,
			session: goodSession,
			m:       goodRewriter,
			store:   goodStore,
			wantErr: ErrInvalidRewrite,
		},
		{
			name: "cancelled context before rewrite returns context error",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			session: goodSession,
			m:       goodRewriter,
			store:   goodStore,
			wantErr: context.Canceled,
		},
		{
			name:    "nil rewriter returns ErrInvalidRewrite",
			ctx:     context.Background(),
			session: goodSession,
			m:       nil,
			store:   goodStore,
			wantErr: ErrInvalidRewrite,
		},
		{
			name:    "typed nil rewriter returns ErrInvalidRewrite",
			ctx:     context.Background(),
			session: goodSession,
			m:       (*fakeRewriter)(nil),
			store:   goodStore,
			wantErr: ErrInvalidRewrite,
		},
		{
			name:    "nil store returns ErrInvalidRewrite",
			ctx:     context.Background(),
			session: goodSession,
			m:       goodRewriter,
			store:   nil,
			wantErr: ErrInvalidRewrite,
		},
		{
			name:    "typed nil store returns ErrInvalidRewrite",
			ctx:     context.Background(),
			session: goodSession,
			m:       goodRewriter,
			store:   (*fakeStore)(nil),
			wantErr: ErrInvalidRewrite,
		},
		{
			name:    "empty ID returns ErrInvalidRewrite",
			ctx:     context.Background(),
			session: Session{ID: "", Summary: "summary", Private: false},
			m:       goodRewriter,
			store:   goodStore,
			wantErr: ErrInvalidRewrite,
		},
		{
			name:    "whitespace-only ID returns ErrInvalidRewrite",
			ctx:     context.Background(),
			session: Session{ID: "   ", Summary: "summary", Private: false},
			m:       goodRewriter,
			store:   goodStore,
			wantErr: ErrInvalidRewrite,
		},
		{
			name:    "empty Summary returns ErrInvalidRewrite",
			ctx:     context.Background(),
			session: Session{ID: "id", Summary: "", Private: false},
			m:       goodRewriter,
			store:   goodStore,
			wantErr: ErrInvalidRewrite,
		},
		{
			name:    "whitespace-only Summary returns ErrInvalidRewrite",
			ctx:     context.Background(),
			session: Session{ID: "id", Summary: "   ", Private: false},
			m:       goodRewriter,
			store:   goodStore,
			wantErr: ErrInvalidRewrite,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RewriteSummary(tt.ctx, tt.session, tt.m, tt.store)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestRewriteSummaryRewriterReturnsError(t *testing.T) {
	rewriter := &fakeRewriter{
		local: true,
		rewrite: func(ctx context.Context, summary string) (string, error) {
			return "", errFake
		},
	}
	store := &fakeStore{}
	session := Session{ID: "id", Summary: "summary", Private: false}

	err := RewriteSummary(context.Background(), session, rewriter, store)
	if !errors.Is(err, errFake) {
		t.Fatalf("expected errFake, got %v", err)
	}
	if store.calls != 0 {
		t.Errorf("expected store not to be called, got %d calls", store.calls)
	}
}

func TestRewriteSummaryEmptyRewrittenText(t *testing.T) {
	tests := []struct {
		name     string
		rewritten string
	}{
		{name: "empty string", rewritten: ""},
		{name: "whitespace only", rewritten: "   \t\n  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rewriter := &fakeRewriter{
				local: true,
				rewrite: func(ctx context.Context, summary string) (string, error) {
					return tt.rewritten, nil
				},
			}
			store := &fakeStore{}
			session := Session{ID: "id", Summary: "summary", Private: false}

			err := RewriteSummary(context.Background(), session, rewriter, store)
			if !errors.Is(err, ErrInvalidRewrite) {
				t.Fatalf("expected ErrInvalidRewrite, got %v", err)
			}
			if store.calls != 0 {
				t.Errorf("expected store not to be called, got %d calls", store.calls)
			}
		})
	}
}

func TestRewriteSummaryContextCancelledAfterRewrite(t *testing.T) {
	// We need a context that is valid during Rewrite but cancelled before PutSummary.
	// We achieve this by cancelling the context inside the Rewrite callback.
	ctx, cancel := context.WithCancel(context.Background())
	rewriter := &fakeRewriter{
		local: true,
		rewrite: func(ctx context.Context, summary string) (string, error) {
			cancel()
			return "rewritten", nil
		},
	}
	store := &fakeStore{}
	session := Session{ID: "id", Summary: "summary", Private: false}

	err := RewriteSummary(ctx, session, rewriter, store)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRewriteSummaryStoreReturnsError(t *testing.T) {
	rewriter := &fakeRewriter{
		local: true,
		rewrite: func(ctx context.Context, summary string) (string, error) {
			return "rewritten", nil
		},
	}
	store := &fakeStore{
		put: func(ctx context.Context, id string, summary string) error {
			return errFake
		},
	}
	session := Session{ID: "id", Summary: "summary", Private: false}

	err := RewriteSummary(context.Background(), session, rewriter, store)
	if !errors.Is(err, errFake) {
		t.Fatalf("expected errFake, got %v", err)
	}
}

func TestIsNilInterface(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want bool
	}{
		{name: "nil", val: nil, want: true},
		{name: "typed nil pointer", val: (*fakeRewriter)(nil), want: true},
		{name: "typed nil store pointer", val: (*fakeStore)(nil), want: true},
		{name: "non-nil pointer", val: &fakeRewriter{}, want: false},
		{name: "non-pointer value int", val: 42, want: false},
		{name: "non-pointer value string", val: "hello", want: false},
		{name: "non-pointer value struct", val: Session{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNilInterface(tt.val)
			if got != tt.want {
				t.Errorf("isNilInterface(%v) = %v, want %v", tt.val, got, tt.want)
			}
		})
	}
}

func TestRewriteSummaryPrivateWithHostedModel(t *testing.T) {
	rewriter := &fakeRewriter{
		local: false,
	}
	store := &fakeStore{}
	session := Session{ID: "id", Summary: "summary", Private: true}

	err := RewriteSummary(context.Background(), session, rewriter, store)
	if !errors.Is(err, ErrHostedPrivate) {
		t.Fatalf("expected ErrHostedPrivate, got %v", err)
	}
	if rewriter.calls != 0 {
		t.Errorf("expected rewriter not called, got %d", rewriter.calls)
	}
	if store.calls != 0 {
		t.Errorf("expected store not called, got %d", store.calls)
	}
}

func TestRewriteSummaryContextDeadlineExceededBeforeRewrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rewriter := &fakeRewriter{local: true}
	store := &fakeStore{}
	session := Session{ID: "id", Summary: "summary", Private: false}

	err := RewriteSummary(ctx, session, rewriter, store)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	fmt.Println("cancelled context test passed")
}
