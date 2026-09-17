package init

// Purpose: the TTY prompter's contract (P1-E16-W4-S35-T6) — what an
//   operator's keystrokes mean, and what an input stream that ends
//   mid-question means.
// Constraints: driven over strings.Reader, never a real terminal.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// promptOver builds a prompter reading answers and capturing the
// questions.
func promptOver(answers string) (*TTYPrompter, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return NewTTYPrompter(strings.NewReader(answers), out), out
}

// TestConfirmReadsAnOperatorsAnswer covers every spelling a person
// actually types, including the bare enter that means "the default".
func TestConfirmReadsAnOperatorsAnswer(t *testing.T) {
	for answer, want := range map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "YES\n": true,
		"n\n": false, "N\n": false, "no\n": false,
	} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			p, _ := promptOver(answer)
			got, err := p.Confirm("Wire it?", !want)
			if err != nil {
				t.Fatalf("Confirm(%q): %v", answer, err)
			}
			if got != want {
				t.Errorf("Confirm(%q) = %v, want %v", answer, got, want)
			}
		})
	}
}

// TestBareEnterTakesTheDefault, in both directions — a prompter that
// always returned false on enter would look correct on every question
// whose default is no.
func TestBareEnterTakesTheDefault(t *testing.T) {
	for _, def := range []bool{true, false} {
		p, out := promptOver("\n")
		got, err := p.Confirm("Install it?", def)
		if err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		if got != def {
			t.Errorf("bare enter with default %v returned %v", def, got)
		}
		wantHint := "[y/N]"
		if def {
			wantHint = "[Y/n]"
		}
		if !strings.Contains(out.String(), wantHint) {
			t.Errorf("the prompt does not show which way enter goes: %q", out)
		}
	}
}

// TestAnAmbiguousConfirmIsRefused: guessing which of yes and no somebody
// meant is how a wizard installs something nobody agreed to.
func TestAnAmbiguousConfirmIsRefused(t *testing.T) {
	p, _ := promptOver("maybe\n")
	if _, err := p.Confirm("Wire it?", true); err == nil {
		t.Fatal("Confirm guessed at an answer that was neither yes nor no")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}

// TestChooseMatchesAnOptionOrRefuses covers the selector: case-insensitive
// match, bare enter for the default, and a refusal for anything else.
func TestChooseMatchesAnOptionOrRefuses(t *testing.T) {
	options := []string{"local", "server", "worker"}

	p, out := promptOver("SERVER\n")
	got, err := p.Choose("Which profile?", options, "local")
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got != "server" {
		t.Errorf("Choose = %q, want the canonical spelling of the matched option", got)
	}
	if !strings.Contains(out.String(), "local/server/worker") {
		t.Errorf("the prompt does not list the options: %q", out)
	}

	p, _ = promptOver("\n")
	if got, err = p.Choose("Which profile?", options, "local"); err != nil || got != "local" {
		t.Errorf("bare enter returned (%q, %v), want the default", got, err)
	}

	p, _ = promptOver("laptop\n")
	if _, err = p.Choose("Which profile?", options, "local"); err == nil {
		t.Fatal("Choose accepted a value that is not one of the options")
	}
}

// TestLineReturnsTheDefaultOnEnter, and shows what that default is —
// an operator pressing enter on a path prompt has to know what they
// agreed to.
func TestLineReturnsTheDefaultOnEnter(t *testing.T) {
	p, out := promptOver("\n")
	got, err := p.Line("Where should the database live?", "/home/db")
	if err != nil {
		t.Fatalf("Line: %v", err)
	}
	if got != "/home/db" {
		t.Errorf("Line = %q, want the default", got)
	}
	if !strings.Contains(out.String(), "/home/db") {
		t.Errorf("the prompt does not show the default: %q", out)
	}

	p, _ = promptOver("  /other/db  \n")
	if got, err = p.Line("Where?", "/home/db"); err != nil || got != "/other/db" {
		t.Errorf("Line = (%q, %v), want the typed value, trimmed", got, err)
	}
}

// TestInputEndingMidQuestionIsRefused is the CI case: a run with no
// terminal must refuse, not silently accept nine defaults.
//
// An operator who wanted the defaults has `--yes`, which says so. A
// prompter that treated a closed stream as agreement would make every
// no-TTY invocation look like an explicit yes.
func TestInputEndingMidQuestionIsRefused(t *testing.T) {
	for name, ask := range map[string]func(*TTYPrompter) error{
		"Confirm": func(p *TTYPrompter) error { _, err := p.Confirm("?", true); return err },
		"Choose":  func(p *TTYPrompter) error { _, err := p.Choose("?", []string{"a"}, "a"); return err },
		"Line":    func(p *TTYPrompter) error { _, err := p.Line("?", "x"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := promptOver("")
			err := ask(p)
			if err == nil {
				t.Fatal("a closed input stream was read as agreement")
			}
			if !errors.Is(err, ErrNoInput) {
				t.Errorf("err = %v, want ErrNoInput", err)
			}
			if !strings.Contains(err.Error(), "--yes") {
				t.Errorf("the refusal does not name the flag that means this: %v", err)
			}
		})
	}
}

// TestTheDefaultPrompterAsksNothing is what makes "zero prompts" a
// property of a type rather than a claim about nine call sites.
func TestTheDefaultPrompterAsksNothing(t *testing.T) {
	p := DefaultPrompter{}
	for _, def := range []bool{true, false} {
		if got, err := p.Confirm("anything", def); err != nil || got != def {
			t.Errorf("Confirm returned (%v, %v), want the default", got, err)
		}
	}
	if got, err := p.Choose("anything", []string{"a", "b"}, "b"); err != nil || got != "b" {
		t.Errorf("Choose returned (%q, %v), want the default", got, err)
	}
	if got, err := p.Line("anything", "d"); err != nil || got != "d" {
		t.Errorf("Line returned (%q, %v), want the default", got, err)
	}
}
