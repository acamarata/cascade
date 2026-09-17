// Purpose: the question tier 2 asks on the attached terminal, and how an
//
//	answer is read back.
//
// WHY ONLY AN EXPLICIT YES APPROVES. Everything else — "n", a blank line,
//
//	a closed terminal, a cancelled context, a word nobody anticipated — is
//	not an approval. An approval parser that accepted anything except a
//	recognised "no" would turn a stray keystroke, or a terminal that
//	closed before the person answered, into consent.
//
// Constraints: bounded. The read runs in its own goroutine so ctx can end
//
//	the wait, because a person who walked away must not hold a daemon
//	thread forever; the goroutine is left to finish its blocked read and
//	its result is discarded, which is the only thing that CAN be done with
//	a blocking terminal read and is why the channel is buffered.
//
// SPORT: fleet.supervision.Tier2Supervisor/ADDED (P1-E18-W4-S39-T3).

package supervision

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/policy"
)

// approvalAnswers are the exact answers that approve. Lower-cased and
// trimmed before the lookup; nothing else in the map, and nothing outside
// it approves.
var approvalAnswers = map[string]bool{"y": true, "yes": true}

// askOnTerminal writes the question to sess and reads one line back.
//
// It returns (true, nil) ONLY on an explicit yes. A read error, a closed
// terminal or a cancelled context returns an error, which the caller turns
// into a denial — never into an approval.
func askOnTerminal(ctx context.Context, sess *Session,
	req policy.EvalRequest, out policy.EvalOutcome) (bool, error) {
	if _, err := fmt.Fprintf(sess.Out,
		"\ncascade: approval needed\n  action:     %s\n  verb:       %s\n  capability: %s\n  risk:       %s\n  reason:     %s\nApprove? [y/N] ",
		actionRef(req), orNone(req.Verb), orNone(req.Capability), out.Level.String(), orNone(out.Reason),
	); err != nil {
		return false, err
	}

	type answer struct {
		line string
		err  error
	}
	// Buffered so the reader goroutine can always finish and exit even
	// after ctx has ended the wait: an unbuffered channel would leak the
	// goroutine forever on every timeout.
	answers := make(chan answer, 1)
	go func() {
		line, err := bufio.NewReader(sess.In).ReadString('\n')
		answers <- answer{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case a := <-answers:
		if a.err != nil && strings.TrimSpace(a.line) == "" {
			// A read error with nothing read is a terminal that went away
			// before the person answered.
			return false, a.err
		}
		return approvalAnswers[strings.ToLower(strings.TrimSpace(a.line))], nil
	}
}

// orNone renders an empty field as something a reader can see, so a prompt
// never shows a blank where a value was expected.
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
