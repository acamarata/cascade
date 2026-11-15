package pbdmode_test

import (
	"fmt"

	"github.com/acamarata/cascade/pkg/pbdmode"
)

// ExampleSlug shows the Epic slug taken from a phase-qualified Epic code.
func ExampleSlug() {
	fmt.Println(pbdmode.Slug("P3-UI"))
	fmt.Println(pbdmode.Slug("P2-PLN-23"))
	fmt.Printf("%q\n", pbdmode.Slug("NODASH"))
	// Output:
	// ui
	// pln-23
	// ""
}
