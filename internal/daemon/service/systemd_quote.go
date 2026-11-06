package service

import "strings"

// systemdQuoteArg quotes s per systemd.syntax(5) whenever it contains
// whitespace or a character that would otherwise corrupt the unit file's
// word-splitting; a plain path with none of those is left unquoted,
// matching real systemd unit files.
func systemdQuoteArg(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\$") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
