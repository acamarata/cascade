package canon

// exitTwoLines maps every verbatim exit2_line string of the capture to
// its class. Nineteen events carry one of these lines (fourteen Blocks,
// five FeedbackOnly); events sharing a line share an entry. The table is
// verbatim, never derived from keywords.
var exitTwoLines = map[string]ExitTwo{
	"show stderr to model and block tool call":                              Blocks,
	"stop the agentic loop (stderr shown to user only)":                     Blocks,
	"block processing, erase original prompt, and show stderr to user only": Blocks,
	"block expansion and show stderr to user only":                          Blocks,
	"show stderr to model and continue conversation":                        Blocks,
	"show stderr to subagent and continue having it run":                    Blocks,
	"block compaction":                         Blocks,
	"block the switch and show stderr to user": Blocks,
	"show stderr to teammate and prevent idle (teammate continues working)": Blocks,
	"show stderr to model and prevent task creation":                        Blocks,
	"show stderr to model and prevent task completion":                      Blocks,
	"deny the elicitation":                               Blocks,
	"block the response (action becomes decline)":        Blocks,
	"block the change from being applied to the session": Blocks,
	"show stderr to model immediately":                   FeedbackOnly,
	"show stderr to user only":                           FeedbackOnly,
}

// otherExitLines maps the verbatim "Other exit codes -" line that gives an
// event without an exit-2 line a class other than NoExitTwo.
var otherExitLines = map[string]ExitTwo{
	"worktree creation failed": AnyNonzeroFails,
}
