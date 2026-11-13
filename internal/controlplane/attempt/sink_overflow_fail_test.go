package attempt

import (
	"fmt"
	"reflect"
	"testing"
)

func TestAppendOverflowFailsClosed(t *testing.T) {
	fmt.Printf("OverflowRow fields:\n")
	tt := reflect.TypeOf(OverflowRow{})
	for i := 0; i < tt.NumField(); i++ {
		f := tt.Field(i)
		fmt.Printf("  %s %s\n", f.Name, f.Type)
	}

	fmt.Printf("AttemptRecord fields:\n")
	tr := reflect.TypeOf(AttemptRecord{})
	for i := 0; i < tr.NumField(); i++ {
		f := tr.Field(i)
		fmt.Printf("  %s %s\n", f.Name, f.Type)
	}

	fmt.Printf("Errors: %v %v %v\n", ErrOverflowWrite, ErrOverflowQueueFull, ErrInvalidOverflow)
}
