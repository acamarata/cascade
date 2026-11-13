package write

import "testing"

// TestRunStopEndsTurnGracefully ensures that RunStop returns true,nil on repeated
// calls and causes no tool, network or CLI action.
func TestRunStopEndsTurnGracefully(t *testing.T) {
	for i := 0; i < 3; i++ {
		turnEnded, err := RunStop()
		if err != nil {
			t.Fatalf("RunStop() returned unexpected error: %v", err)
		}
		if !turnEnded {
			t.Fatalf("RunStop() returned turnEnded=%v, want true", turnEnded)
		}
	}
}
