// Package violation (bare_println_violation.go) proves the bare
// fmt.Println case — the implicitly-stdout print family this gate must
// catch even though it names no os.Stdout identifier at all.
package violation

import "fmt"

func GreetBare(name string) {
	fmt.Println("hello, " + name)
}
