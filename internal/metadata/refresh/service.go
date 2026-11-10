// Package refresh maintains the local bangumi-data reference index.
package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/metadata/matcher"
	metadataport "bansoko/internal/metadata/port"
	observabilityport "bansoko/internal/observability/port"
)

const (
	packageURL      = "https://unpkg.com/bangumi-data@0.3/package.json"
	exactDataPrefix = "https://unpkg.com/bangumi-data@"
	maxPackageBytes = 1 << 20
	maxDataBytes    = 32 << 20
	checkInterval   = 6 * time.Hour
	retryInterval   = 5 * time.Minute
)

var upstreamVersionPattern = regexp.MustCompile(`^0\.3\.[0-9]+$`)

type IndexPublisher interface {
	ReplaceBangumiDataIndex(*matcher.BangumiDataIndex)
}

type Service struct {
	Store         metadataport.SnapshotStore
	Publisher     IndexPublisher
	Client        *http.Client
	Logger        observabilityport.RuntimeLogger
	Now           func() time.Time
	CheckInterval time.Duration
	RetryInterval time.Duration
	available     bool
}

func NewService(store metadataport.SnapshotStore, publisher IndexPublisher, client *http.Client, logger observabilityport.RuntimeLogger, available bool) *Service {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	return &Service{Store: store, Publisher: publisher, Client: client, Logger: logger, Now: time.Now, CheckInterval: checkInterval, RetryInterval: retryInterval, available: available}
}

// Restore reads only local state. Network access belongs to Run.
func Restore(ctx context.Context, store metadataport.SnapshotStore) (*matcher.BangumiDataIndex, error) {
	if store == nil {
		return nil, nil
	}
	snapshot, ok, err := store.Load(ctx)
	if err != nil || !ok {
		return nil, err
	}
	return matcher.ParseBangumiData(snapshot.Payload)
}

// Run serializes checks and uses the last successful check time across restarts.
func (s *Service) Run(ctx context.Context) {
	if s == nil || s.Store == nil || s.Publisher == nil {
		return
	}
	delay := time.Duration(0)
	checkEvery, retryAfter := s.CheckInterval, s.RetryInterval
	if checkEvery <= 0 {
		checkEvery = checkInterval
	}
	if retryAfter <= 0 {
		retryAfter = retryInterval
	}
	if s.available {
		if snapshot, ok, err := s.Store.Load(ctx); err == nil && ok {
			delay = snapshot.CheckedAt.Add(checkEvery).Sub(s.now())
		}
	}
	for {
		if !wait(ctx, delay) {
			return
		}
		if err := s.Refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.Logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message: "bangumi-data refresh failed; keeping the previous index", Component: "runtime",
				Operation: "bangumi_data_refresh", Outcome: "warning", ReasonCode: "bangumi_data_refresh_failed", ErrorClass: "metadata_matcher",
			})
			delay = retryAfter
		} else {
			delay = checkEvery
		}
	}
}

// Refresh probes the small package manifest and downloads data only when the
// upstream 0.3.x version changed (or no usable local index exists).
func (s *Service) Refresh(ctx context.Context) error {
	if s == nil || s.Store == nil || s.Publisher == nil {
		return fmt.Errorf("bangumi-data refresh is not configured")
	}
	now := s.now()
	current, found, err := s.Store.Load(ctx)
	if err != nil {
		return err
	}
	body, _, err := s.get(ctx, packageURL, maxPackageBytes)
	if err != nil {
		return err
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("decode bangumi-data package manifest: %w", err)
	}
	manifest.Version = strings.TrimSpace(manifest.Version)
	if !upstreamVersionPattern.MatchString(manifest.Version) {
		return fmt.Errorf("bangumi-data package has an unsupported version")
	}
	if found && s.available && current.UpstreamVersion == manifest.Version {
		return s.Store.Touch(ctx, now)
	}
	dataURL := exactDataPrefix + url.PathEscape(manifest.Version) + "/dist/data.json"
	payload, lastModified, err := s.get(ctx, dataURL, maxDataBytes)
	if err != nil {
		return err
	}
	index, err := matcher.ParseBangumiData(payload)
	if err != nil {
		return err
	}
	if err := s.Store.Save(ctx, metadataport.Snapshot{
		UpstreamVersion: manifest.Version, Payload: payload, LastModified: lastModified,
		FetchedAt: now, CheckedAt: now,
	}); err != nil {
		return err
	}
	s.Publisher.ReplaceBangumiDataIndex(index)
	s.available = true
	return nil
}

func (s *Service) get(ctx context.Context, rawURL string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "static-json-client/1.0")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch bangumi-data: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("bangumi-data CDN returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("read bangumi-data response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("bangumi-data response exceeds %d bytes", limit)
	}
	lastModified := resp.Header.Get("Last-Modified")
	if len(lastModified) > 256 {
		lastModified = ""
	}
	return body, lastModified, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func wait(ctx context.Context, delay time.Duration) bool {
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
