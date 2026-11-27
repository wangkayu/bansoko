package adapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	provider "bansoko/internal/provider/domain"
	"bansoko/internal/provider/outbound"
)

func TestAniBTGroupReadOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, body, retry string
		code              int
		state             provider.State
		reason            provider.ReasonCode
	}{
		{"indexed empty", `{"ok":true,"data":{"bgmId":123,"total":0,"groups":[]}}`, "", 200, provider.StateEmpty, ""},
		{"missing groups", `{"ok":true,"data":{"bgmId":123,"total":0}}`, "", 200, provider.StateError, provider.ReasonGroupReadFailed},
		{"incomplete", `{"data":{"bgmId":123,"total":0,"groups":[]}}`, "", 200, provider.StateError, provider.ReasonObservationIncomplete},
		{"not indexed", `{"error":{"code":"ANIME_NOT_FOUND"}}`, "", 404, provider.StateNotIndexed, provider.ReasonAnimeNotFound},
		{"other 404", `{"error":{"code":"NOT_FOUND"}}`, "", 404, provider.StateError, provider.ReasonUpstreamStatus},
		{"non-404 marker", `{"error":{"code":"ANIME_NOT_FOUND"}}`, "", 500, provider.StateError, provider.ReasonUpstreamStatus},
		{"limited", "", "45", 429, provider.StateRateLimited, provider.ReasonRateLimited},
		{"unavailable", "", "45", 503, provider.StateError, provider.ReasonRetryAfter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Retry-After", tc.retry)
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			scheduler, err := outbound.New(ctx, outbound.DefaultPolicies(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			reader := AniBTGroupReader{Endpoint: server.URL, Client: server.Client(), Outbound: scheduler}
			result, err := reader.ReadGroups(ctx, "123")
			status := provider.GroupReadStatus(result)
			if err != nil {
				status = provider.ClassifyReadError(err, time.Now())
			}
			if status.State != tc.state || status.ReasonCode != tc.reason {
				t.Fatalf("status = %+v (err=%v), want %s/%s", status, err, tc.state, tc.reason)
			}
			if tc.retry != "" {
				if status.RetryAfter != 45*time.Second || !status.CooldownUntil.Equal(status.CheckedAt.Add(status.RetryAfter)) {
					t.Fatalf("unexpected upstream timing: %+v", status)
				}
				_, err := reader.ReadGroups(outbound.WithPriority(ctx, outbound.PriorityInteractive), "123")
				refused := provider.ClassifyReadError(err, time.Now())
				if refused.State != provider.StateCooldown || !refused.CooldownUntil.Equal(status.CooldownUntil) || hits.Load() != 1 {
					t.Fatalf("cooldown = %+v, requests = %d", refused, hits.Load())
				}
			}
		})
	}
}
