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
}

type SubmissionResult struct {
	Task             domain.ProviderTask
	Accepted         bool
	ResponseReceived bool
	StatusCode       int
}

type SubmissionPreparer interface {
	PrepareSubmission(context.Context, []byte, string, domain.PathSnapshot, string) (*PreparedSubmission, domain.ProviderTask, bool, error)
}

func (c ConfiguredClient) PrepareSubmission(ctx context.Context, payload []byte, filename string, snapshot domain.PathSnapshot, key string) (*PreparedSubmission, domain.ProviderTask, bool, error) {
	client, err := c.clientForSnapshot(ctx, snapshot)
	if err != nil {
		return nil, domain.ProviderTask{}, false, err
	}
	return client.PrepareSubmission(ctx, payload, filename, snapshot, key)
}

func (c Client) PrepareSubmission(ctx context.Context, payload []byte, filename string, snapshot domain.PathSnapshot, key string) (*PreparedSubmission, domain.ProviderTask, bool, error) {
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
	return &PreparedSubmission{client: &mutationClient, endpoint: strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/add", body: body.Bytes(), contentType: writer.FormDataContentType(), snapshot: normalized, tag: tag}, domain.ProviderTask{}, false, nil
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
	response, err := p.client.Do(request)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("submit qB task: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	result := SubmissionResult{ResponseReceived: true, StatusCode: response.StatusCode}
	if err != nil {
		return result, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || strings.TrimSpace(string(body)) != "Ok." {
		return result, fmt.Errorf("qB task submission failed with HTTP %d", response.StatusCode)
	}
	result.Accepted = true
	snapshot := p.snapshot
	result.Task = domain.ProviderTask{ID: "tag:" + p.tag, State: domain.TaskSubmitted, PathSnapshot: &snapshot}
	return result, nil
}
