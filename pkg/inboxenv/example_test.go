package inboxenv_test

import (
	"fmt"

	"github.com/acamarata/cascade/pkg/inboxenv"
)

// ExampleEncode summarises an inbox body, encodes the envelope and prints
// the summary and the bounded JSON encoding.
func ExampleEncode() {
	env := inboxenv.Envelope{
		ID:          "evt-1",
		Class:       "message",
		OriginScope: "session",
		DataClass:   "internal",
		Trust:       "untrusted",
		Summary:     inboxenv.Summarize([]byte("hello world")),
	}
	encoded, err := inboxenv.Encode(env)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(env.Summary)
	fmt.Println(string(encoded))
	// Output:
	// excerpt 5/11 chars: hello…
	// {"id":"evt-1","class":"message","origin_scope":"session","data_class":"internal","trust":"untrusted","summary":"excerpt 5/11 chars: hello…"}
}
