// Package violation is a seeded-violation fixture for the no-sleep gate
// (Art.7.3). Never built or linted — lives under testdata/.
package violation

import "time"

func BusyWait() {
	time.Sleep(time.Second)
}
