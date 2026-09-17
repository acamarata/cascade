package init

// Purpose: the three Prompter implementations (P1-E16-W4-S35-T6) — a
//   real TTY reader, and the two that answer for the operator.
// Constraints: --yes and --check are whole Prompters rather than flags
//   the steps check, so no step can forget one of them. Neither ever
//   returns anything but the default, which is what makes "zero prompts"
//   a property of the type rather than a claim about the code.
// SPORT: internal/runtime/init prompters (ADD) — P1-E16-W4-S35-T6.

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultPrompter answers every question with its default and asks
// nothing. It backs both --yes and --check.
type DefaultPrompter struct{}

var _ Prompter = DefaultPrompter{}

// Confirm returns def.
func (DefaultPrompter) Confirm(_ string, def bool) (bool, error) { return def, nil }

// Choose returns def.
func (DefaultPrompter) Choose(_ string, _ []string, def string) (string, error) { return def, nil }

// Line returns def.
func (DefaultPrompter) Line(_, def string) (string, error) { return def, nil }

// TTYPrompter reads answers from a terminal.
type TTYPrompter struct {
	in  *bufio.Reader
	out io.Writer
}

var _ Prompter = (*TTYPrompter)(nil)

// NewTTYPrompter builds a prompter over in and out.
func NewTTYPrompter(in io.Reader, out io.Writer) *TTYPrompter {
	return &TTYPrompter{in: bufio.NewReader(in), out: out}
}

// ErrNoInput is the refusal when the input stream ends mid-question.
//
// A refusal rather than "assume the default": the operator is being asked
// because the answer matters, and an input stream that closed is not the
// same as an operator who pressed enter.
var ErrNoInput = cascade.New(cascade.KindInvalidInput,
	"cascade init: input ended while a question was still open; use --yes to accept every default")

// Confirm asks a yes/no question.
func (p *TTYPrompter) Confirm(question string, def bool) (bool, error) {
	suffix := " [y/N] "
	if def {
		suffix = " [Y/n] "
	}
	answer, err := p.ask(question + suffix)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, cascade.Newf(cascade.KindInvalidInput,
			"cascade init: %q is not yes or no", answer)
	}
}

// Choose asks for one of options.
func (p *TTYPrompter) Choose(question string, options []string, def string) (string, error) {
	answer, err := p.ask(fmt.Sprintf("%s [%s] (default %s) ", question, strings.Join(options, "/"), def))
	if err != nil {
		return "", err
	}
	if answer == "" {
		return def, nil
	}
	for _, o := range options {
		if strings.EqualFold(o, answer) {
			return o, nil
		}
	}
	return "", cascade.Newf(cascade.KindInvalidInput,
		"cascade init: %q is not one of %v", answer, options)
}

// Line asks for free text.
func (p *TTYPrompter) Line(question, def string) (string, error) {
	prompt := question + " "
	if def != "" {
		prompt = fmt.Sprintf("%s (default %s) ", question, def)
	}
	answer, err := p.ask(prompt)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

// ask writes the prompt and reads one line.
func (p *TTYPrompter) ask(prompt string) (string, error) {
	if _, err := io.WriteString(p.out, prompt); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "cascade init: write a prompt")
	}
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		if err == io.EOF {
			return "", ErrNoInput
		}
		return "", cascade.Wrap(cascade.KindUnavailable, err, "cascade init: read an answer")
	}
	return strings.TrimSpace(line), nil
}
