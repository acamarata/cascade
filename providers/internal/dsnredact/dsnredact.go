// Package dsnredact is the one redactor for connection strings and
// connection-stage driver errors in providers/**. Every SQL provider (and
// any provider that reports a URL it dialed) calls it, so the rules that
// keep a credential out of an error cannot drift between copies.
//
// Purpose: render a DSN or URL without its credentials, list the secrets a
// DSN carries, and wrap or detach a pgx connection error so that neither
// its text nor its Unwrap chain can carry a DSN credential.
//
// Inputs: a DSN (postgres URL or key=value) or a URL with known schemes,
// and the driver error a dial or parse returned for it. The DSN only names
// the secrets that must stay out; it is never interpolated.
//
// Outputs: a redacted rendering or placeholder; a *cascade.Error whose
// cause is a credential-free stand-in (connCause).
//
// Constraints: fail closed. A form the redactor cannot parse becomes the
// fixed placeholder. The raw pgx error never enters a returned chain: pgx
// v5.10.0 echoes the password in some ParseConfigError texts,
// ParseConfigError exports its ConnString and ConnectError its Config.
// Lives under providers/internal so only providers/** can import it
// (Art.10.2 keeps providers off the module-root internal/**).
//
// SPORT: providers.internal.dsnredact/ADDED.
package dsnredact

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
)

// placeholder stands in for a DSN that cannot be shown safely. withheld is
// a connection error's whole cause text when that text could carry a
// secret.
const (
	placeholder = "<redacted-dsn>"
	withheld    = "driver error detail withheld: it could carry a credential"
)

// connCause is the credential-free cause of a connection-stage error. Its
// text holds no DSN secret, and it unwraps only to context.Canceled or
// context.DeadlineExceeded (or nothing), never to the driver error, so
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

// Redact renders a postgres DSN without credentials: a postgres URL keeps
// its scheme, user, host and path, its password shows as xxxxx and its
// query (where pgx also reads password=) is dropped. Any other form, a DSN
// pgx cannot parse, or a rendering that still holds a secret gives the
// placeholder.
func Redact(dsn string) string {
	secrets, ok := Secrets(dsn)
	if !ok {
		return placeholder
	}
	return render(dsn, secrets, "postgres", "postgresql")
}

// RedactURL renders raw, a URL whose scheme is one of schemes, the way
// Redact renders a postgres URL. Its secrets are the userinfo and every
// password or sslpassword query value raw spells; no driver parse is
// needed. Any other scheme, an unparseable URL or a rendering that still
// holds a secret gives the placeholder.
func RedactURL(raw string, schemes ...string) string {
	return render(raw, append(rawDSNSecrets(raw), raw, strings.TrimSpace(raw)), schemes...)
}

// render is the one rendering path behind Redact and RedactURL. It keeps
// only scheme, user, host and path, masks a set password, drops the query
// and fragment, and checks the result against secrets before returning it.
// Only a "scheme://" input renders: without the authority marker net/url
// reads the userinfo as path (redis:/u:pw@h keeps u:pw@h verbatim), and the
// scanners behind secrets never see it.
func render(dsn string, secrets []string, schemes ...string) string {
	u, err := url.Parse(dsn)
	if err != nil || len(secrets) == 0 || !slices.Contains(schemes, u.Scheme) ||
		!strings.HasPrefix(strings.ToLower(dsn), u.Scheme+"://") || atOutsideAuthority(dsn) {
		return placeholder
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
	return placeholder
}

// atOutsideAuthority reports whether the last '@' after "://" sits past the
// first '/', '?' or '#'. net/url and pgx then end the authority early and
// read the head of an unescaped password (u:4242?pw@h parses as host u,
// port 4242), so a rendering would print that head. render fails closed
// on it. A legitimate '@' in a query value is over-redacted.
func atOutsideAuthority(dsn string) bool {
	_, rest, _ := strings.Cut(dsn, "://") // no "://": rest is empty, so no '@'
	at := strings.LastIndexByte(rest, '@')
	end := strings.IndexAny(rest, "/?#")
	return at >= 0 && end >= 0 && at > end
}

// Secrets lists what error text must never hold for a postgres dsn: the
// raw DSN, every password pgx would send (PGPASSWORD and the passfile
// included), PGSSLPASSWORD, and every password and sslpassword dsn spells,
// as written and decoded, each in every passwordForms shape. ok is false
// when pgx cannot parse dsn, or when dsn is a URL whose last '@' sits past
// the authority (atOutsideAuthority): pgx then reads the password's head
// as a port and its tail as the path, and its connect errors print both
// (host:port, database=...) in shapes no secret list can name. The secrets
// are then unknown and callers fail closed: WrapConn and Detach withhold
// the driver text.
func Secrets(dsn string) (secrets []string, ok bool) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil || atOutsideAuthority(dsn) {
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

// URLCause returns the cause a URL driver may keep for err, a dial or
// handshake error for raw, a URL whose scheme is one of schemes. It is err
// itself only when raw renders (RedactURL gives no placeholder) and err's
// text holds none of raw's secrets. Otherwise it is a credential-free
// stand-in that unwraps only to a context sentinel: a URL that does not
// render (no "scheme://", an '@' past the authority, a password that is
// also the host) can put part of a password in the address a dial error
// prints. A nil err returns nil.
func URLCause(err error, raw string, schemes ...string) error {
	if err == nil {
		return nil
	}
	secrets := append(rawDSNSecrets(raw), raw, strings.TrimSpace(raw))
	if RedactURL(raw, schemes...) == placeholder || holdsSecret(err.Error(), secrets) {
		return newConnCause(withheld, err)
	}
	return err
}

// WrapConn wraps a connection-stage error (Open, Ping) under kind, the
// caller's classification. The message is format and args followed by
// Redact(dsn). The cause is the driver text when Secrets knows dsn's
// secrets and the text holds none of them; otherwise (always for a
// ParseConfigError, which can echo the password, and for a URL with an
// '@' past the authority) it is withheld. The raw driver error is never
// kept. A nil err returns nil.
func WrapConn(err error, dsn string, kind cascade.Kind, format string, args ...any) error {
	if err == nil {
		return nil
	}
	text := withheld
	var parseErr *pgconn.ParseConfigError
	if !errors.As(err, &parseErr) {
		if secrets, ok := Secrets(dsn); ok && !holdsSecret(err.Error(), secrets) {
			text = err.Error()
		}
	}
	msg := fmt.Sprintf(format, args...) + " " + Redact(dsn)
	return cascade.Wrap(kind, newConnCause(text, err), msg)
}

// Detach returns err unchanged unless its chain holds a pgx
// connection-stage error, which a pooled reconnect can surface after Open.
// Then it returns a connCause holding the error text when that text holds
// none of secrets (the Secrets of the DSN the store was opened with; nil
// when unknown, which withholds) and none of the ConnectError's own Config
// password forms. Otherwise, and always for a ParseConfigError, the text
// is withheld. Either way the Config and the ConnString leave the chain.
func Detach(err error, secrets []string) error {
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
