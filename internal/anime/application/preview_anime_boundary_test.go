package application

import (
	"testing"
)

func TestCloneStringSlicePreservesNilAndEmpty(t *testing.T) {
	var nilValue []string
	empty := []string{}
	for name, input := range map[string][]string{"nil": nilValue, "empty": empty} {
		t.Run(name, func(t *testing.T) {
			cloned := cloneStringSlice(input)
			if (cloned == nil) != (input == nil) {
				t.Fatalf("clone nil = %v, want %v", cloned == nil, input == nil)
			}
		})
	}
}
