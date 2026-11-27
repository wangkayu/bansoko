package outbound

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type recordingRoundTripper func(*http.Request) (*http.Response, error)

func (f recordingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConcurrentMetadataHTTPStartsRespectSlidingWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scheduler := testScheduler(t, nil, nil)
		starts := make(chan time.Time, 41)
		client := &http.Client{Transport: recordingRoundTripper(func(*http.Request) (*http.Response, error) {
			starts <- time.Now()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})}
		var workers sync.WaitGroup
		ready := make(chan struct{})
		for i := range 41 {
			workers.Go(func() {
				<-ready
				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("https://anibt.net/api/anime/groups?entry=%d", i), nil)
				if err != nil {
					t.Error(err)
					return
				}
				response, err := Do(client, scheduler, AniBTMetadata, request)
				if err != nil {
					t.Error(err)
					return
				}
				response.Body.Close()
			})
		}
		close(ready)
		workers.Wait()
		close(starts)
		timestamps := slices.SortedFunc(func(yield func(time.Time) bool) {
			for start := range starts {
				if !yield(start) {
					return
				}
			}
		}, time.Time.Compare)
		if len(timestamps) != 41 {
			t.Fatalf("actual sends = %d, want 41", len(timestamps))
		}
		for i := 30; i < len(timestamps); i++ {
			if elapsed := timestamps[i].Sub(timestamps[i-30]); elapsed < time.Minute {
				t.Fatalf("31 sends in %v", elapsed)
			}
		}
	})
}

func TestQueuedMetadataHTTPRequestsShareObservedCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scheduler := testScheduler(t, nil, nil)
		var sends int
		client := &http.Client{Transport: recordingRoundTripper(func(*http.Request) (*http.Response, error) {
			sends++
			status := http.StatusOK
			header := http.Header{}
			if sends == 1 {
				time.Sleep(time.Second)
				status = http.StatusTooManyRequests
				header.Set("Retry-After", "7")
			}
			return &http.Response{StatusCode: status, Header: header, Body: http.NoBody}, nil
		})}
		send := func(path string) error {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://anibt.net"+path, nil)
			if err != nil {
				return err
			}
			response, err := Do(client, scheduler, AniBTMetadata, request)
			if response != nil {
				response.Body.Close()
			}
			return err
		}
		season := make(chan error, 1)
		groups := make(chan error, 1)
		start := time.Now()
		go func() { season <- send("/api/seasons/anime") }()
		synctest.Wait()
		go func() { groups <- send("/api/anime/groups") }()
		var actual *ThrottleError
		var local *CooldownError
		if err := <-season; !errors.As(err, &actual) || actual.StatusCode != 429 {
			t.Fatalf("HTTP outcome = %v", err)
		}
		if err := <-groups; !errors.As(err, &local) || !local.CooldownUntil.Equal(start.Add(8*time.Second)) || sends != 1 {
			t.Fatalf("queued outcome = %v, sends = %d", err, sends)
		}
		time.Sleep(7 * time.Second)
		if err := send("/api/anime/groups"); err != nil || sends != 2 {
			t.Fatalf("send after cooldown = %v, sends = %d", err, sends)
		}
	})
}
