package pkg

// Widget is the fixture's exported type.
type Widget struct {
	Name string
}

// Foo is the fixture's exported function.
func Foo() string {
	return "foo"
}

// unexportedHelper is never emitted as a node.
func unexportedHelper() string {
	return "helper"
}
