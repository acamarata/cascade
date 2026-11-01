package canon

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// shMeta lists the characters POSIX sh treats as syntax when unquoted.
const shMeta = "$`|&<>()\\\"*?[]#~=%{}!\n\t"

// errShSyntax reports a command that leaves the expected sh grammar.
var errShSyntax = errors.New("sh words: unexpected syntax")

// shLexer holds the tokeniser state for splitShCommands.
type shLexer struct {
	cmds      [][]string
	words     []string
	cur, raw  strings.Builder
	inWord    bool
	inQuote   bool
	escaped   bool
	unquoteOK bool
}

// flush ends the current word, refusing unquoted syntax other than ">&2".
func (l *shLexer) flush() error {
	if !l.inWord {
		return nil
	}
	if !l.unquoteOK && l.raw.String() != ">&2" {
		return errShSyntax
	}
	l.words = append(l.words, l.cur.String())
	l.cur.Reset()
	l.raw.Reset()
	l.inWord, l.unquoteOK = false, true
	return nil
}

// step consumes one rune of the command.
func (l *shLexer) step(r rune) error {
	switch {
	case l.escaped:
		l.escaped = false
		l.cur.WriteRune(r)
		l.raw.WriteRune(r)
	case !l.inQuote && r == '\\':
		l.escaped, l.inWord = true, true
		l.raw.WriteRune(r)
	case l.inQuote && r == '\'':
		l.inQuote = false
	case l.inQuote:
		l.cur.WriteRune(r)
	case r == '\'':
		l.inQuote, l.inWord = true, true
		l.raw.WriteRune(r)
	case r == ' ' || r == ';':
		if err := l.flush(); err != nil {
			return err
		}
		if r == ';' {
			l.cmds = append(l.cmds, l.words)
			l.words = nil
		}
	default:
		if strings.ContainsRune(shMeta, r) {
			l.unquoteOK = false
		}
		l.inWord = true
		l.cur.WriteRune(r)
		l.raw.WriteRune(r)
	}
	return nil
}

// splitShCommands splits cmd the way POSIX sh tokenises a simple list of
// commands built only from single-quoted, backslash-escaped and plain
// words. It returns one word slice per ';'-separated command and refuses
// any unquoted shell syntax other than the literal redirection ">&2", so
// an unescaped quote in a reason surfaces as an error or a changed list.
func splitShCommands(cmd string) ([][]string, error) {
	l := &shLexer{unquoteOK: true}
	for _, r := range cmd {
		if err := l.step(r); err != nil {
			return nil, err
		}
	}
	if l.inQuote || l.escaped {
		return nil, errShSyntax
	}
	if err := l.flush(); err != nil {
		return nil, err
	}
	return append(l.cmds, l.words), nil
}

// assertShWords checks that cmd parses under sh into exactly the printf
// of reason to stderr followed by exit 2.
func assertShWords(t *testing.T, cmd, reason string) {
	t.Helper()
	got, err := splitShCommands(cmd)
	if err != nil {
		t.Fatalf("reason %q: command %q does not parse: %v", reason, cmd, err)
	}
	want := [][]string{
		{"printf", `%s\n`, reason, ">&2"},
		{"exit", strconv.Itoa(RefusalExitCode)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reason %q: sh words %q, want %q", reason, got, want)
	}
}

func TestSplitShCommandsRefusesUnescapedQuote(t *testing.T) {
	bad := []string{
		"printf '%s\\n' 'it's blocked' >&2; exit 2",
		"printf '%s\\n' x >&2; exit 2; touch pwned",
		"printf '%s\\n' $(touch pwned) >&2; exit 2",
	}
	for _, cmd := range bad {
		got, err := splitShCommands(cmd)
		if err == nil && len(got) == 2 && len(got[0]) == 4 && len(got[1]) == 2 {
			t.Fatalf("command %q parsed as a clean refusal: %q", cmd, got)
		}
	}
}
