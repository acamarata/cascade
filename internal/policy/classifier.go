// Package policy (classifier.go): Purpose: CommandClassifier, the seam the
//
//	policy evaluation engine calls to put a shell command on the
//	06-FORGE-SPEC §5.15 risk ladder before anything is allowed to run it.
//
// Inputs: a context (cancellation only; classification is pure computation
//
//	with no clock and no I/O) and one shell command string.
//
// Outputs: a RiskLevel, and on refusal a *ClassifyError whose level is
//
//	always L4.
//
// Constraints: FAIL CLOSED in every direction §5.15 names. Input that will
//
//	not parse, an empty command, a command name that is not a static
//	literal, an AST node form this file does not model, and a command name
//	the table does not carry all return L4. There is no permissive
//	fallthrough and no default rung: the switch that assigns a level is
//	total over the table and everything outside the table is refused.
//	Windows-native forms (PowerShell cmdlets, cmd.exe builtins) are NOT
//	special-cased and there is no build-tagged classification code
//	(R-14.28): they reach the same table, miss, and are refused as
//	(L4, classify-unknown) like any other unrecognised form.
//	There is no second dangerous-command list in this repository to derive
//	from: internal/rpc's elevationTable enumerates elevated RPC VERBS, a
//	different vocabulary from shell argv, and nothing else in the tree
//	enumerates shell commands. The table below is therefore the first one,
//	and it is asserted against a verbatim transcription of the §5.15 prose
//	in classifier_test.go rather than against itself.
//
// SPORT: internal/policy CommandClassifier/ADDED (P1-E09-W2-S17-T3).
package policy

import (
	"context"
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// CommandClassifier resolves the §5.15 risk level of a shell command.
// R-14.26 makes this the single seam through which a level is resolved,
// once, before the policy layers are evaluated.
type CommandClassifier interface {
	// Classify returns the command's rung. It returns L4 with a
	// *ClassifyError whenever the command cannot be classified; it never
	// returns a permissive level together with an error.
	Classify(ctx context.Context, cmd string) (RiskLevel, error)
}

// NewCommandClassifier returns the production classifier, which parses
// with mvdan.cc/sh and walks the resulting AST.
func NewCommandClassifier() CommandClassifier { return mvdanClassifier{} }

// mvdanClassifier is the real implementation. Its only state is the
// wrapper-recursion depth, so a zero value is safe to share across
// goroutines.
type mvdanClassifier struct {
	// depth counts how many wrapper strings deep this classification is,
	// so `sh -c "sh -c ..."` cannot recurse without bound.
	depth uint8
}

// Classify implements CommandClassifier.
func (c mvdanClassifier) Classify(ctx context.Context, cmd string) (RiskLevel, error) {
	if err := ctx.Err(); err != nil {
		return L4, newUnknownError("classification was canceled before it completed")
	}
	if strings.TrimSpace(cmd) == "" {
		return L4, newUnknownError("the command is empty")
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.KeepComments(false))
	file, err := parser.Parse(strings.NewReader(cmd), "command")
	if err != nil {
		return L4, newParseError(err)
	}
	if reason, ok := unmodeledForm(file); !ok {
		return L4, newUnknownError(reason)
	}
	return c.classifyStmts(ctx, file.Stmts)
}

// modeledNodes is the closed set of AST node forms this classifier
// reasons about. Every other form the grammar can produce (function
// declarations, loops, conditionals, case clauses, arithmetic, process
// substitution, extended globs, brace expansion, declare/let/time/coproc)
// is something the table cannot see through, so it is refused rather than
// walked past. Growing this set is a deliberate act with a matching table
// rule, never an accident.
var modeledNodes = map[string]bool{
	"*syntax.File": true, "*syntax.Stmt": true, "*syntax.CallExpr": true,
	"*syntax.Word": true, "*syntax.Lit": true, "*syntax.SglQuoted": true,
	"*syntax.DblQuoted": true, "*syntax.CmdSubst": true, "*syntax.ParamExp": true,
	"*syntax.Assign": true, "*syntax.Redirect": true, "*syntax.BinaryCmd": true,
	"*syntax.Subshell": true, "*syntax.Block": true,
}

// unmodeledForm walks the whole tree once and reports the first node form
// outside modeledNodes. It descends into command substitutions, because
// their contents really do run, and stops at parameter expansions, whose
// interior is opaque by nature and is handled where the word is resolved.
func unmodeledForm(file *syntax.File) (string, bool) {
	found := ""
	syntax.Walk(file, func(n syntax.Node) bool {
		if n == nil || found != "" {
			return false
		}
		name := nodeTypeName(n)
		if !modeledNodes[name] {
			found = "the command uses " + strings.TrimPrefix(name, "*syntax.") +
				", a shell form this classifier does not model"
			return false
		}
		return name != "*syntax.ParamExp"
	})
	if found != "" {
		return found, false
	}
	return "", true
}

// classifyStmts returns the most restrictive level across a list of
// statements. A line is as dangerous as its most dangerous member, which
// is what stops `ls && rm -rf /` from being read as a listing.
func (c mvdanClassifier) classifyStmts(ctx context.Context, stmts []*syntax.Stmt) (RiskLevel, error) {
	if len(stmts) == 0 {
		return L4, newUnknownError("the command contains no statement to classify")
	}
	worst := L0
	for _, stmt := range stmts {
		level, err := c.classifyStmt(ctx, stmt)
		if err != nil {
			return L4, err
		}
		worst = maxLevel(worst, level)
	}
	return worst, nil
}

// classifyStmt classifies one statement together with its redirections.
func (c mvdanClassifier) classifyStmt(ctx context.Context, stmt *syntax.Stmt) (RiskLevel, error) {
	if stmt == nil || stmt.Cmd == nil {
		return L4, newUnknownError("the command contains an empty statement")
	}
	level, err := c.classifyCommand(ctx, stmt.Cmd)
	if err != nil {
		return L4, err
	}
	redirLevel, err := redirectLevel(stmt.Redirs)
	if err != nil {
		return L4, err
	}
	return maxLevel(level, redirLevel), nil
}

// classifyCommand dispatches on the modeled command forms.
func (c mvdanClassifier) classifyCommand(ctx context.Context, cmd syntax.Command) (RiskLevel, error) {
	switch node := cmd.(type) {
	case *syntax.CallExpr:
		return c.classifyCall(ctx, node)
	case *syntax.BinaryCmd:
		return c.classifyStmts(ctx, []*syntax.Stmt{node.X, node.Y})
	case *syntax.Subshell:
		return c.classifyStmts(ctx, node.Stmts)
	case *syntax.Block:
		return c.classifyStmts(ctx, node.Stmts)
	default:
		return L4, newUnknownError("the command uses " +
			strings.TrimPrefix(nodeTypeName(cmd), "*syntax.") +
			", a command form this classifier does not model")
	}
}

// classifyCall classifies a single invocation: its environment prefix, the
// substitutions embedded in its words, and the command itself.
func (c mvdanClassifier) classifyCall(ctx context.Context, call *syntax.CallExpr) (RiskLevel, error) {
	assignLevel, err := assignmentLevel(call.Assigns)
	if err != nil {
		return L4, err
	}
	substLevel, err := c.substitutionLevel(ctx, call)
	if err != nil {
		return L4, err
	}
	if len(call.Args) == 0 {
		// An assignment with no command: shell state only.
		return maxLevel(assignLevel, substLevel), nil
	}
	name, ok := resolveWord(call.Args[0])
	if !ok || name == "" {
		return L4, newUnknownError(
			"the command name is not a static literal, so what would run cannot be determined")
	}
	base := path.Base(name)
	level, err := c.classifyInvocation(ctx, base, call.Args[1:])
	if err != nil {
		return L4, err
	}
	return maxLevel(level, maxLevel(assignLevel, substLevel)), nil
}

// classifyInvocation resolves one argv against the wrapper forms first and
// the §5.15 command table second. A name in neither is refused.
func (c mvdanClassifier) classifyInvocation(
	ctx context.Context, base string, args []*syntax.Word,
) (RiskLevel, error) {
	if _, isWrapper := wrapperTable[base]; isWrapper {
		return c.classifyWrapper(ctx, base, args)
	}
	rule, known := commandTable[base]
	if !known {
		return L4, newUnknownError("the command " + quoteName(base) +
			" is not in the risk table, so its effects are unknown")
	}
	return rule.level(base, args)
}

// substitutionLevel classifies every command substitution embedded in a
// call's words and assignments. Those commands run, so `ls $(rm -rf /)` is
// as dangerous as the deletion it hides.
func (c mvdanClassifier) substitutionLevel(
	ctx context.Context, call *syntax.CallExpr,
) (RiskLevel, error) {
	worst := L0
	var walkErr error
	visit := func(n syntax.Node) bool {
		if walkErr != nil {
			return false
		}
		sub, ok := n.(*syntax.CmdSubst)
		if !ok {
			return true
		}
		level, err := c.classifyStmts(ctx, sub.Stmts)
		if err != nil {
			walkErr = err
			return false
		}
		worst = maxLevel(worst, level)
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
	if walkErr != nil {
		return L4, walkErr
	}
	return worst, nil
}
