//go:build postgres

// Purpose: the pgvector error seam. It keeps every DSN credential out of
// the text and the chain of an error from the pgx database/sql driver.
//
// Inputs: a driver error and, at the connection stage, the DSN it was
// dialed with (later, that DSN's secret set). The DSN only names the
// secrets that must stay out; it is never interpolated.
//
// Outputs: a *cascade.Error. wrapConnError maps PgError 28P01/42501 to
// KindPermissionDenied, *pgconn.ParseConfigError to KindInvalidInput and
// anything else to KindUnavailable; its message carries redactDSN(dsn) only.
//
// Constraints: fail closed. The raw pgx error never enters the returned
// chain: pgx v5.10.0 echoes the password in some ParseConfigError texts,
// ParseConfigError exports its ConnString and ConnectError its Config.
// The cause is a connCause instead: driver text that holds no DSN secret,
// or withheld when it might, unwrapping only to a context sentinel.
//
// SPORT: providers.pgvector.wrapConnError/ADDED.

package pgvector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maskedDSN stands in for a DSN that cannot be shown safely. withheld is a
// connection error's whole cause text when that text could carry a secret.
const (
	maskedDSN = "<redacted-dsn>"
	withheld  = "driver error detail withheld: it could carry a credential"
)

// connCause is the credential-free cause of a connection-stage error. Its
// text holds no DSN secret, and it unwraps only to context.Canceled or
// context.DeadlineExceeded (or nothing), never to the pgx error, so
// errors.As cannot reach a ConnectError's Config or a ParseConfigError's
// ConnString through it.
type connCause struct {
	text string
	ctx  error
}

// Error returns the checked cause text.
func (c *connCause) Error() string { return c.text }

// Unwrap exposes only the context sentinel, so errors.Is(err,
// context.DeadlineExceeded) keeps working on a timed-out connect.
func (c *connCause) Unwrap() error { return c.ctx }

// newConnCause builds a connCause with text, keeping from err only the
// context sentinel its chain carries.
func newConnCause(text string, err error) *connCause {
	c := &connCause{text: text}
	switch {
	case errors.Is(err, context.Canceled):
		c.ctx = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		c.ctx = context.DeadlineExceeded
	}
	return c
}

// wrapConnError wraps a connection-stage error (Open, Ping). PgError
// 28P01/42501 is KindPermissionDenied, *pgconn.ParseConfigError
// KindInvalidInput, anything else KindUnavailable. The message is format
// and args followed by redactDSN(dsn). The cause is the driver text when
// pgx can parse dsn and the text holds none of its secrets; otherwise
// (always for a ParseConfigError, which can echo the password) it is
// withheld.
func wrapConnError(err error, dsn, format string, args ...any) error {
	kind, text := classifyPgError(err), withheld
	if kind != cascade.KindPermissionDenied {
		kind = cascade.KindUnavailable
	}
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		kind = cascade.KindInvalidInput
	} else if secrets, ok := dsnSecrets(dsn); ok && !holdsSecret(err.Error(), secrets) {
		text = err.Error()
	}
	msg := fmt.Sprintf(format, args...) + " " + redactDSN(dsn)
	return cascade.Wrap(kind, newConnCause(text, err), msg)
}

// redactDSN renders dsn without credentials: a postgres URL keeps its
// scheme, user, host and path, its password shows as xxxxx and its query
// (where pgx also reads password=) is dropped. Any other form, a DSN pgx
// cannot parse, or a rendering that still holds a secret gives maskedDSN.
func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	secrets, ok := dsnSecrets(dsn)
	if err != nil || !ok || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return maskedDSN
	}
	out := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	if u.User != nil {
		out.User = url.User(u.User.Username())
		if _, set := u.User.Password(); set {
			out.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	if s := out.String(); !holdsSecret(s, secrets) {
		return s
	}
	return maskedDSN
}

// dsnSecrets lists what error text must never hold for dsn: the raw DSN,
// every password pgx would send (PGPASSWORD and the passfile included),
// PGSSLPASSWORD, and every password and sslpassword dsn spells, as written
// and decoded (rawDSNSecrets), each in every passwordForms shape. ok is
// false when pgx cannot parse dsn: its secrets are then unknown and
// callers fail closed.
func dsnSecrets(dsn string) (secrets []string, ok bool) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return nil, false
	}
	secrets = append(passwordForms(cfg.Password), passwordForms(os.Getenv("PGSSLPASSWORD"))...)
	for _, raw := range append([]string{dsn, strings.TrimSpace(dsn)}, rawDSNSecrets(dsn)...) {
		if raw != "" {
			secrets = append(secrets, raw)
		}
	}
	return secrets, true
}

// rawDSNSecrets lists each password and sslpassword dsn spells, as written
// and decoded. Both grammars are scanned whatever dsn looks like, since a
// DSN can carry a userinfo and a query password that differ and pgx sends
// only one: over-redaction costs diagnostics, a miss leaks.
func rawDSNSecrets(dsn string) []string {
	out := kvSecrets(dsn)
	if _, rest, isURL := strings.Cut(dsn, "://"); isURL {
		out = append(out, urlSecrets(rest)...)
	}
	return out
}

// urlSecrets scans rest, a URL DSN after "://": its userinfo whole and its
// password, both bounded by the authority and by the last '@', and every
// password or sslpassword query value.
func urlSecrets(rest string) (out []string) {
	auth := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		auth = rest[:i]
	}
	for _, s := range []string{auth, rest} {
		if at := strings.LastIndexByte(s, '@'); at >= 0 {
			if _, pw, set := strings.Cut(s[:at], ":"); set && pw != "" {
				out = append(append(out, decodedForms(s[:at])...), decodedForms(pw)...)
			}
		}
	}
	_, query, _ := strings.Cut(rest, "?")
	query, _, _ = strings.Cut(query, "#")
	for _, pair := range strings.FieldsFunc(query, func(r rune) bool { return r == '&' || r == ';' }) {
		if k, v, _ := strings.Cut(pair, "="); isPasswordKey(k) {
			out = append(out, decodedForms(v)...)
		}
	}
	return out
}

// kvSecrets scans dsn as key=value settings the way pgx does and lists
// each password and sslpassword value as written (quotes and backslashes
// kept), unquoted, unescaped as pgx does (only \\ and \') and as libpq does
// (a backslash escapes any byte, so p\ w means "p w").
func kvSecrets(dsn string) (out []string) {
	const space = " \t\n\r\v\f"
	for s := strings.TrimLeft(dsn, space); s != ""; {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := s[:eq]
		s = strings.TrimLeft(s[eq+1:], space)
		end := kvValueEnd(s, space)
		if isPasswordKey(key) {
			raw := s[:end]
			inner := strings.TrimSuffix(strings.TrimPrefix(raw, "'"), "'")
			plain := strings.ReplaceAll(strings.ReplaceAll(inner, `\\`, `\`), `\'`, `'`)
			out = append(append(append(out, decodedForms(raw)...), decodedForms(inner)...), passwordForms(plain)...)
			out = append(out, passwordForms(kvEscape.ReplaceAllString(inner, "$1"))...)
		}
		s = strings.TrimLeft(s[end:], space)
	}
	return out
}

// kvEscape matches a libpq key=value escape: a backslash and the byte after.
var kvEscape = regexp.MustCompile(`\\([\s\S])`)

// kvValueEnd returns the length of the key=value value s starts with:
// through the closing quote of a quoted value, else up to the first
// unescaped space byte; a backslash escapes the next byte, as in pgx.
func kvValueEnd(s, space string) int {
	quoted := strings.HasPrefix(s, "'")
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case quoted && i > 0 && c == '\'':
			return i + 1
		case !quoted && strings.IndexByte(space, c) >= 0:
			return i
		}
	}
	return len(s)
}

// isPasswordKey reports whether a DSN key (raw or query-escaped, any case)
// names a password or sslpassword.
func isPasswordKey(k string) bool {
	if d, err := url.QueryUnescape(k); err == nil {
		k = d
	}
	k = strings.ToLower(strings.TrimSpace(k))
	return k == "password" || k == "sslpassword"
}

// decodedForms lists raw as written and path- and query-decoded, each in
// every passwordForms shape.
func decodedForms(raw string) []string {
	out := passwordForms(raw)
	for _, decode := range []func(string) (string, error){url.PathUnescape, url.QueryUnescape} {
		if d, err := decode(raw); err == nil {
			out = append(out, passwordForms(d)...)
		}
	}
	return out
}

// passwordForms lists p raw and query-, path- and userinfo-escaped: the
// shapes an error text could carry it in. An empty p has none.
func passwordForms(p string) []string {
	if p == "" {
		return nil
	}
	return []string{p, url.QueryEscape(p), url.PathEscape(p), url.User(p).String()}
}

// holdsSecret reports whether text contains any of secrets. A hit means
// the text is withheld or masked whole; it is never patched, since a
// patched text would show where the secret sat.
func holdsSecret(text string, secrets []string) bool {
	for _, s := range secrets {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// detachConnError returns err unchanged unless its chain holds a pgx
// connection-stage error, which a pooled reconnect can surface after Open.
// Then it returns a connCause holding the error text when that text holds
// none of secrets (the dsnSecrets of the DSN the store was opened with;
// empty when unknown, which withholds) and none of the ConnectError's own
// Config password forms. Otherwise, and always for a ParseConfigError
// (which can echo it), the text is withheld. Either way the Config and
// the ConnString leave the chain.
func detachConnError(err error, secrets []string) error {
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		return newConnCause(withheld, err)
	}
	var connErr *pgconn.ConnectError
	if !errors.As(err, &connErr) {
		return err
	}
	text := withheld
	if len(secrets) > 0 && connErr.Config != nil &&
		!holdsSecret(err.Error(), append(passwordForms(connErr.Config.Password), secrets...)) {
		text = err.Error()
	}
	return newConnCause(text, err)
}

// wrapDBError wraps a query-stage error under classifyPgError's Kind. A
// connection-stage error from a pooled reconnect is detached first against
// secrets (the store's dsnSecrets), so no call site can hand a caller the
// driver's Config, raw DSN or any DSN password.
func wrapDBError(err error, secrets []string, format string, args ...any) error {
	return cascade.Wrapf(classifyPgError(err), detachConnError(err, secrets), format, args...)
}
