// Purpose: the DSN scanners behind Secrets and RedactURL. They list every
// spelling of each password and sslpassword a DSN carries, in both the URL
// and the key=value grammar, raw and decoded.
//
// Constraints: over-redaction costs diagnostics, a miss leaks, so both
// grammars are scanned whatever the DSN looks like.

package dsnredact

import (
	"net/url"
	"regexp"
	"strings"
)

// rawDSNSecrets lists each password and sslpassword dsn spells, as written
// and decoded. Both grammars are scanned whatever dsn looks like, since a
// DSN can carry a userinfo and a query password that differ and pgx sends
// only one.
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
