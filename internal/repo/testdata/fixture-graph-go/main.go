package main

import "fixturegraphgo/pkg"

// Version is the fixture's exported package-level var, exercised by the
// GraphNodeKind=var emission path.
var Version = "v1"

// Run calls into pkg.Foo, exercising the imports edge.
func Run() string {
	return pkg.Foo()
}

func main() {
	Run()
}
