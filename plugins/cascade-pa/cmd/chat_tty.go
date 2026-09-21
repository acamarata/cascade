package cmd

// Purpose (this file): isRealTTY, split out of chat.go under Art.10.3's
//   300-line cap once U/S-46.T4's non-interactive `--thread <slug>` parity
//   check pushed chat.go over it.
// Inputs: an io.Reader (cc.InOrStdin()).
// Outputs: whether it is a real, interactively-readable character device.
// Constraints: stdlib only (os.ModeCharDevice) -- no new dependency for a
//   single boolean check.
// SPORT: plugin.cascade-pa:cmd:chat (CHANGE) -- P1-E21-W5-S46-T4.

import (
	"io"
	"os"
)

// isRealTTY reports whether in is a character device this process could
// read interactively from -- the standard, dependency-free Go idiom
// (os.ModeCharDevice), used ONLY to gate `--thread <slug>` with no prompt
// and no --json between "print a text summary and exit" (automation
// parity, 06-FORGE-SPEC §5.8) and "open the TUI". A non-*os.File in
// (every test's bytes.Buffer, and any future non-file cc.InOrStdin())
// reports false -- never a TTY -- rather than guessing, matching chat.go's
// existing fail-toward-non-interactive posture for CASCADE_NO_INPUT.
func isRealTTY(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}
