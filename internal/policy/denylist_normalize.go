// Package policy (denylist_normalize.go): Purpose: R-21.212's recursive
//
//	normalization — turning one command string into every canonical argv
//	form it would actually run, so a deny-listed operation reached through
//	a wrapper, a nested shell, a command substitution, a chained operator,
//	an environment-assignment prefix or shell quoting still matches the
//	same pattern the bare form matches.
//
// Inputs: a command string, and the recursion depth already spent.
// Outputs: the forms to match, each flagged partial when an argument could
//
//	not be read, or a refusal.
//
// Constraints: the classifier already refuses every form this file would
//
//	have to guess at, and this file refuses the same ones for the same
//	reasons: input that will not parse, an AST form the classifier does
//	not model, and a command NAME that is not a static literal are all
//	errors here, which layer 1 reads as a deny. An ARGUMENT that will not
//	resolve is not an error: it produces a PARTIAL form, which matches any
//	pattern its unread tail could have completed. Both directions fail
//	closed; neither invents a form that is not there.
//
//	The word helpers are the classifier's own (resolveWord, path.Base,
//	stripXargsFlags, stripSSHFlags). There is no second unescaper here.
//
// SPORT: internal/policy denylist-normalizer/ADDED (P1-E09-W2-S17-T4).
package policy

import (
	"context"
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// denyForm is one canonical argv rendering of something the command would
// run. Partial marks a form whose tail could not be read.
type denyForm struct {
	text    string
	partial bool
}

// shellWrappers are the wrapper names whose -c string is another command.
var shellWrappers = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
}

// normalizedForms returns every form to match cmd against. The trimmed
// original is always the first form, so an operator who writes the exact
// string they saw still matches even if this file's rendering differs.
func normalizedForms(ctx context.Context, cmd string) ([]denyForm, error) {
	return collectForms(ctx, cmd, 0)
}

// collectForms is normalizedForms carrying the wrapper-recursion depth, so
// a nested shell cannot reset the bound by starting a fresh collector.
func collectForms(ctx context.Context, cmd string, depth uint8) ([]denyForm, error) {
	if err := ctx.Err(); err != nil {
		return nil, newUnknownError("normalization was canceled before it completed")
	}
	if depth >= maxWrapperDepth {
		return nil, newUnknownError("the command nests wrappers more deeply than can be normalized")
	}
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return nil, newUnknownError("the command is empty")
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.KeepComments(false))
	file, err := parser.Parse(strings.NewReader(trimmed), "command")
	if err != nil {
		return nil, newParseError(err)
	}
	if reason, ok := unmodeledForm(file); !ok {
		return nil, newUnknownError(reason)
	}
	c := &formCollector{ctx: ctx, depth: depth, forms: []denyForm{{text: trimmed}}}
	if err := c.stmts(file.Stmts); err != nil {
		return nil, err
	}
	return c.forms, nil
}

// formCollector accumulates the forms of one normalization. depth bounds
// wrapper recursion exactly as the classifier's own maxWrapperDepth does.
type formCollector struct {
	// ctx carries cancellation into every nested normalization.
	ctx   context.Context
	forms []denyForm
	depth uint8
}

// emit records a form, skipping an exact duplicate so a chained command
// does not grow the list quadratically.
func (c *formCollector) emit(f denyForm) {
	if f.text == "" {
		return
	}
	for _, seen := range c.forms {
		if seen.text == f.text && seen.partial == f.partial {
			return
		}
	}
	c.forms = append(c.forms, f)
}

// stmts collects the forms of a statement list.
func (c *formCollector) stmts(stmts []*syntax.Stmt) error {
	for _, stmt := range stmts {
		if stmt == nil || stmt.Cmd == nil {
			return newUnknownError("the command contains an empty statement")
		}
		if err := c.command(stmt.Cmd); err != nil {
			return err
		}
	}
	return nil
}

// command dispatches on the modeled command forms. Anything else is the
// classifier's own refusal, restated rather than guessed at.
func (c *formCollector) command(cmd syntax.Command) error {
	switch node := cmd.(type) {
	case *syntax.CallExpr:
		return c.call(node)
	case *syntax.BinaryCmd:
		return c.stmts([]*syntax.Stmt{node.X, node.Y})
	case *syntax.Subshell:
		return c.stmts(node.Stmts)
	case *syntax.Block:
		return c.stmts(node.Stmts)
	default:
		return newUnknownError("the command uses " +
			strings.TrimPrefix(nodeTypeName(cmd), "*syntax.") +
			", a command form that cannot be normalized")
	}
}

// call collects one invocation: the substitutions hidden in its words, the
// invocation itself under both its written and its base name, and the
// inner command of any wrapper it names. The environment-assignment prefix
// is dropped, which is what makes `A=1 rm -rf /` normalize to `rm -rf /`.
func (c *formCollector) call(call *syntax.CallExpr) error {
	if err := c.substitutions(call); err != nil {
		return err
	}
	if len(call.Args) == 0 {
		return nil
	}
	name, ok := resolveWord(call.Args[0])
	if !ok || name == "" {
		return newUnknownError(
			"the command name is not a static literal, so what would run cannot be normalized")
	}
	args, partial := resolveArgs(call.Args[1:])
	base := path.Base(name)
	c.emit(denyForm{text: join(base, args), partial: partial})
	if base != name {
		c.emit(denyForm{text: join(name, args), partial: partial})
	}
	return c.inner(base, call.Args[1:])
}

// substitutions collects the forms of every command substitution embedded
// in a call's words and assignments. Those commands really do run.
func (c *formCollector) substitutions(call *syntax.CallExpr) error {
	var walkErr error
	visit := func(n syntax.Node) bool {
		if walkErr != nil {
			return false
		}
		sub, ok := n.(*syntax.CmdSubst)
		if !ok {
			return true
		}
		walkErr = c.stmts(sub.Stmts)
		return false
	}
	for _, word := range call.Args {
		syntax.Walk(word, visit)
	}
	for _, assign := range call.Assigns {
		if assign.Value != nil {
			syntax.Walk(assign.Value, visit)
		}
	}
	return walkErr
}

// inner descends into the wrapper forms the classifier resolves, so the
// command a wrapper would run is matched as itself.
func (c *formCollector) inner(base string, args []*syntax.Word) error {
	if c.depth >= maxWrapperDepth {
		return newUnknownError("the command nests wrappers more deeply than can be normalized")
	}
	switch {
	case shellWrappers[base]:
		return c.shellInner(base, args)
	case base == "xargs":
		rest, err := stripXargsFlags(args)
		if err != nil {
			return err
		}
		return c.wrapped(rest)
	case base == "ssh":
		rest, err := stripSSHFlags(args)
		if err != nil {
			return err
		}
		if len(rest) < 2 {
			return nil
		}
		return c.wrapped(rest[1:])
	}
	return nil
}

// shellInner re-normalizes the -c string of a nested shell.
func (c *formCollector) shellInner(base string, args []*syntax.Word) error {
	for i, arg := range args {
		value, ok := resolveWord(arg)
		if !ok {
			return newUnknownError("an argument of " + quoteName(base) +
				" is not a static literal, so the shell's input cannot be normalized")
		}
		if value != "-c" || i+1 >= len(args) {
			continue
		}
		script, ok := resolveWord(args[i+1])
		if !ok {
			return newUnknownError("the " + quoteName(base+" -c") +
				" command string is not a static literal, so what the shell would run " +
				"cannot be normalized")
		}
		if strings.TrimSpace(script) == "" {
			return nil
		}
		forms, err := collectForms(c.ctx, script, c.depth+1)
		if err != nil {
			return err
		}
		for _, f := range forms {
			c.emit(f)
		}
		return nil
	}
	return nil
}

// wrapped collects the form of an inner argv a wrapper hands to exec.
func (c *formCollector) wrapped(args []*syntax.Word) error {
	if len(args) == 0 {
		return nil
	}
	name, ok := resolveWord(args[0])
	if !ok || name == "" {
		return newUnknownError(
			"the inner command of a wrapper is not a static literal, so it cannot be normalized")
	}
	rest, partial := resolveArgs(args[1:])
	base := path.Base(name)
	c.emit(denyForm{text: join(base, rest), partial: partial})
	if base != name {
		c.emit(denyForm{text: join(name, rest), partial: partial})
	}
	deeper := &formCollector{ctx: c.ctx, depth: c.depth + 1}
	if err := deeper.inner(base, args[1:]); err != nil {
		return err
	}
	for _, f := range deeper.forms {
		c.emit(f)
	}
	return nil
}

// resolveArgs renders the argument words that can be read literally,
// stopping at the first one that cannot and reporting the form partial.
func resolveArgs(args []*syntax.Word) ([]string, bool) {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		value, ok := resolveWord(arg)
		if !ok {
			return out, true
		}
		out = append(out, value)
	}
	return out, false
}

// join renders a command name and its arguments as one canonical string.
func join(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}
