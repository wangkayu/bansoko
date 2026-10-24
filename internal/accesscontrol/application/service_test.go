package application

import "testing"

func TestGeneratedTokensAreUnpredictableLength(t *testing.T) {
	first, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || len(second) != 64 || first == second {
		t.Fatalf("unexpected tokens: %d, %d, equal=%v", len(first), len(second), first == second)
	}
	if hashToken(first) == first {
		t.Fatal("raw bootstrap token must not equal its persisted hash")
	}
}
