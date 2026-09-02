// Package violation (sleep_alias_violation.go) proves the no-sleep gate
// resolves an aliased "time" import, not merely a literal "time.Sleep"
// selector.
package violation

import tt "time"

func BusyWaitAliased() {
	tt.Sleep(tt.Second)
}
