// Package summaryrewrite provides utilities for rewriting session summaries.
package summaryrewrite

import (
	"context"
	"errors"
	"reflect"
	"strings"
)

// ErrInvalidRewrite is returned when validation of the rewrite inputs fails.
var ErrInvalidRewrite = errors.New("invalid summary rewrite")

// ErrHostedPrivate is returned when a private session attempts to use a hosted model.
var ErrHostedPrivate = errors.New("private summary requires local model")

// Session represents a user session with an identifier, summary text, and privacy flag.
type Session struct {
	// ID is the unique identifier for the session.
	ID string
	// Summary is the current summary text of the session.
	Summary string
	// Private indicates whether the session requires local-only processing.
	Private bool
}

// Rewriter defines the interface for rewriting session summaries.
type Rewriter interface {
	// Local reports whether this rewriter runs on a local model.
	Local() bool
	// Rewrite transforms the given summary text using the provided context.
	Rewrite(ctx context.Context, summary string) (string, error)
}

// Store defines the interface for persisting rewritten summaries.
type Store interface {
	// PutSummary stores the rewritten summary for the given session ID.
	PutSummary(ctx context.Context, id string, summary string) error
}

// RewriteSummary validates inputs, checks privacy constraints, rewrites the
// session summary using the provided Rewriter, and persists the result via Store.
func RewriteSummary(ctx context.Context, s Session, m Rewriter, store Store) error {
	if ctx == nil {
		return ErrInvalidRewrite
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if isNilInterface(m) || isNilInterface(store) {
		return ErrInvalidRewrite
	}

	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Summary) == "" {
		return ErrInvalidRewrite
	}

	if s.Private && !m.Local() {
		return ErrHostedPrivate
	}

	rewritten, err := m.Rewrite(ctx, s.Summary)
	if err != nil {
		return err
	}

	if strings.TrimSpace(rewritten) == "" {
		return ErrInvalidRewrite
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return store.PutSummary(ctx, s.ID, rewritten)
}

// isNilInterface reports whether v is a nil interface or a typed nil pointer.
func isNilInterface(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
