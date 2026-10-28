package qbittorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"bansoko/internal/download/domain"
)

// PreparedSubmission owns one frozen request. Execute consumes it once, including
// network failures; reading or retrying a database receipt never resends the POST.
type PreparedSubmission struct {
	mu          sync.Mutex
	used        bool
	client      *http.Client
	endpoint    string
	body        []byte
	contentType string
	snapshot    domain.PathSnapshot
	tag         string
	// lookup re-reads the frozen hashes after a refused add so the refusal of a
	// torrent another task already holds is attributed to that task.
	lookup *http.Client
	qb     Client
	hashes []string
}

type SubmissionResult struct {
	Task             domain.ProviderTask
	Accepted         bool
	ResponseReceived bool
	StatusCode       int
}

// SubmissionPreparer prepares one frozen add. hashes are the frozen info hashes
// of the payload (HashCandidates form); when non-empty, a task already holding
// one of them without the operation tag refuses the submission with
// ErrHashOccupied before any add is sent. An empty list skips that check (the
// cross-owner adoption path, which submits nothing).
type SubmissionPreparer interface {
	PrepareSubmission(context.Context, []byte, string, domain.PathSnapshot, string, []string) (*PreparedSubmission, domain.ProviderTask, bool, error)
}

func (c ConfiguredClient) PrepareSubmission(ctx context.Context, payload []byte, filename string, snapshot domain.PathSnapshot, key string, hashes []string) (*PreparedSubmission, domain.ProviderTask, bool, error) {
	client, err := c.clientForSnapshot(ctx, snapshot)
	if err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	return client.PrepareSubmission(ctx, payload, filename, snapshot, key, hashes)
}

func (c Client) PrepareSubmission(ctx context.Context, payload []byte, filename string, snapshot domain.PathSnapshot, key string, hashes []string) (*PreparedSubmission, domain.ProviderTask, bool, error) {
	normalized, err := NormalizePathSnapshot(snapshot)
	if err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	c, err = c.withSnapshot(normalized)
	if err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	if err := validateFileSubmitInput(c.Endpoint, payload, filename, normalized.RemoteRoot, key); err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	tag := "bansoko-" + key
	item, found, err := c.findByTag(ctx, client, tag)
	if err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	if found {
		return nil, c.providerTaskWithSnapshot(ctx, client, "tag:"+tag, item, &normalized), true, nil
	}
	hashes = HashCandidates(hashes...)
	occupied, err := c.hashOccupied(ctx, client, hashes, tag)
	if err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	if occupied {
		return nil, domain.ProviderTask{}, false, ErrHashOccupied
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("torrents", filename)
	if err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	if _, err := part.Write(payload); err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	fields := [][2]string{{"savepath", filepath.Clean(normalized.RemoteRoot)}, {"tags", strings.Join(append([]string{tag}, c.UserTags...), ",")}}
	if strings.TrimSpace(c.Category) != "" {
		fields = append(fields, [2]string{"category", c.Category})
	}
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return nil, domain.ProviderTask{}, false, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	mutationClient := *client
	mutationClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &PreparedSubmission{client: &mutationClient, endpoint: strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/add", body: body.Bytes(), contentType: writer.FormDataContentType(), snapshot: normalized, tag: tag, lookup: client, qb: c, hashes: hashes}, domain.ProviderTask{}, false, nil
}

func (p *PreparedSubmission) Execute(ctx context.Context) (SubmissionResult, error) {
	if p == nil {
		return SubmissionResult{}, errors.New("nil prepared qB submission")
	}
	p.mu.Lock()
	if p.used {
		p.mu.Unlock()
		return SubmissionResult{}, errors.New("qB submission request already consumed")
	}
	p.used = true
	p.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(p.body))
	if err != nil {
		return SubmissionResult{}, err
	}
	request.Header.Set("Content-Type", p.contentType)
	response, err := doOutbound(p.client, request)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("submit qB task: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	result := SubmissionResult{ResponseReceived: true, StatusCode: response.StatusCode}
	if err != nil {
		return result, err
	}
	if response.StatusCode == http.StatusForbidden {
		return result, fmt.Errorf("qB task submission refused with HTTP 403: %w", ErrAuthFailed)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || strings.TrimSpace(string(body)) != "Ok." {
		// qB refuses a torrent it already holds (4.x: 200 "Fails.", 5.x: 409).
		// Only those two refusals are attributed to occupation, and only when a
		// re-read finds a task for the frozen hashes without this submission's
		// tag; any other answer, or a failed re-read, stays an ordinary failure.
		refused := (response.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "Fails.") || response.StatusCode == http.StatusConflict
		if refused && p.lookup != nil && len(p.hashes) > 0 {
			if occupied, lookupErr := p.qb.hashOccupied(ctx, p.lookup, p.hashes, p.tag); lookupErr == nil && occupied {
				return result, fmt.Errorf("%w (add refused with HTTP %d)", ErrHashOccupied, response.StatusCode)
			}
		}
		return result, fmt.Errorf("qB task submission failed with HTTP %d", response.StatusCode)
	}
	result.Accepted = true
	snapshot := p.snapshot
	result.Task = domain.ProviderTask{ID: "tag:" + p.tag, State: domain.TaskSubmitted, PathSnapshot: &snapshot}
	return result, nil
}
