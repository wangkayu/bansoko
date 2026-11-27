package application

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"bansoko/internal/metadata/matcher"
	provider "bansoko/internal/provider/domain"
	"bansoko/internal/provider/outbound"
)

func TestMetadataFailureState(t *testing.T) {
	limited := &matcher.MatchError{Kind: matcher.ErrorProvider, Layer: "bgm", Err: &provider.ReadFailure{Status: provider.Status{State: provider.StateRateLimited}}}
	cooldown := &matcher.MatchError{Kind: matcher.ErrorProvider, Layer: "tmdb", Err: &outbound.CooldownError{}}
	unavailable := &matcher.MatchError{Kind: matcher.ErrorProvider, Layer: "tmdb", Err: &outbound.ThrottleError{StatusCode: http.StatusServiceUnavailable}}
	for _, tc := range []struct {
		name string
		err  error
		want provider.State
	}{
		{"BGM 429", limited, provider.StateRateLimited},
		{"TMDB local wait", cooldown, provider.StateCooldown},
		{"TMDB 503", unavailable, provider.StateError},
		{"wrapped quota only", fmt.Errorf("match: %w", errors.Join(cooldown, limited)), provider.StateRateLimited},
		{"wrapped mixed outcomes", fmt.Errorf("match: %w", errors.Join(limited, unavailable)), provider.StateError},
		{"joined persistence error", errors.Join(cooldown, errors.New("persist binding failed")), provider.StateError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := metadataFailureState(tc.err); got != tc.want {
				t.Fatalf("state=%s, want %s (error: %v)", got, tc.want, tc.err)
			}
		})
	}
}
