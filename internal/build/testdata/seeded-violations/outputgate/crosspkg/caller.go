// Package fixturecaller (crosspkg/caller.go) is the R-14.137 cross-package
// proof's caller half: it contains NO direct os/fmt reference at all — it
// only calls fixturehelper.WriteBanner — which is exactly what makes the
// indirection an evasion of any scan that inspects only the call site.
// This file must scan clean; helper.go must not.
package fixturecaller

import "cascade/internal/build/testdata/seeded-violations/outputgate/crosspkg/fixturehelper"

func Run(msg string) {
	fixturehelper.WriteBanner(msg)
}
