package application

import (
	"errors"
	"testing"
)

func TestSubtitleBindingFailurePreservesMissingAmbiguousBeforeFinalSet(t *testing.T) {
	for _, tt := range []struct {
		state string
		want  error
		code  string
	}{{bindingStateMissing, ErrSubtitleBindingMissing, "subtitle_binding_missing"}, {bindingStateAmbiguous, ErrSubtitleBindingAmbiguous, "subtitle_binding_ambiguous"}} {
		got := subtitleBindingFailure(tt.state)
		if !errors.Is(got, tt.want) || failureCode(got) != tt.code {
			t.Fatalf("%s: %v/%s", tt.state, got, failureCode(got))
		}
	}
	if subtitleBindingFailure(bindingStateBound) != nil {
		t.Fatal("bound classified as failure")
	}
}
