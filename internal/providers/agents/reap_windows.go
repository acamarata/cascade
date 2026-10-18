package agents

// Purpose: the windows stub. Windows has no portable way to read another
// process's environment block without CGO, so the lease sweep is
// unsupported there and says so rather than reporting a sweep it did not do.

import "github.com/acamarata/cascade/pkg/cascade"

// platformSource is KindUnsupported on windows.
func platformSource() (procSource, error) {
	return nil, cascade.New(cascade.KindUnsupported, "agents: the lease sweep is not supported on windows")
}
