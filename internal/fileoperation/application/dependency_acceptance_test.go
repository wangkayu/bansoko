package application

import "testing"

// S4 must invoke this assertion after real typed predecessor Finish, never a
// fixture-created continuation. Kept separate from the prepare phase acceptance.
func assertS4R63Continuations(t *testing.T, count int) {
	t.Helper()
	if count != 2 {
		t.Fatalf("continuations=%d", count)
	}
}
