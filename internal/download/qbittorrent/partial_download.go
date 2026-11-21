package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/download/domain"
)

var ErrPartialDownloadIdentity = errors.New("cannot verify the partial-download task or its files in qBittorrent")
var ErrPartialDownloadPending = errors.New("qBittorrent has not confirmed the partial download is ready")

type PartialSubmissionPreparer interface {
	PreparePartialSubmission(context.Context, []byte, string, domain.PathSnapshot, string, []string, string) (*PreparedSubmission, domain.ProviderTask, bool, error)
}

type PartialSubmissionLookup interface {
	LookupPartialSubmission(context.Context, string, domain.PathSnapshot, string, []string) (domain.ProviderTask, bool, error)
}

func (c ConfiguredClient) LookupPartialSubmission(ctx context.Context, operationKey string, snapshot domain.PathSnapshot, namespace string, hashes []string) (domain.ProviderTask, bool, error) {
	client, err := c.partialClient(ctx, snapshot, namespace)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	return client.LookupPartialSubmission(ctx, operationKey, snapshot, namespace, hashes)
}

func (c Client) LookupPartialSubmission(ctx context.Context, operationKey string, snapshot domain.PathSnapshot, _ string, hashes []string) (domain.ProviderTask, bool, error) {
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return domain.ProviderTask{}, false, err
	}
	tag := "bansoko-" + operationKey
	item, found, err := c.findByTag(ctx, client, tag)
	if err != nil || !found {
		return domain.ProviderTask{}, false, err
	}
	if !matchesPartialTask(item, tag, hashes, snapshot) {
		return domain.ProviderTask{}, false, ErrPartialDownloadIdentity
	}
	return c.buildProviderTask("tag:"+tag, item, nil, &snapshot), true, nil
}

func matchesPartialTask(item TorrentInfo, tag string, hashes []string, snapshot domain.PathSnapshot) bool {
	return hasTag(item.Tags, tag) && slices.Contains(hashes, strings.ToLower(item.Hash)) &&
		filepath.IsAbs(item.SavePath) && filepath.Clean(item.SavePath) == filepath.Clean(snapshot.RemoteRoot)
}

type PartialDownloadObserver interface {
	ObservePartialDownload(context.Context, PartialDownloadRequest) (domain.ProviderTask, error)
}

type PartialDownloadRequest struct {
	TaskID      string
	Hashes      []string
	Namespace   string
	Snapshot    domain.PathSnapshot
	Selection   domain.PartialDownload
	Manifest    map[string]int64
	StartSent   bool
	BeforeWrite func(ctx context.Context, start bool) (time.Duration, error)
}

func initialStopField(version string) (string, error) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) >= 2 {
		major, majorErr := strconv.Atoi(parts[0])
		minor, minorErr := strconv.Atoi(parts[1])
		if majorErr == nil && minorErr == nil {
			if major == 5 && minor >= 0 {
				return "stopped", nil
			}
			if major == 4 && minor >= 6 {
				return "paused", nil
			}
		}
	}
	return "", errors.New("qBittorrent version does not support verified stopped adds")
}

func (c ConfiguredClient) partialClient(ctx context.Context, snapshot domain.PathSnapshot, namespace string) (Client, error) {
	if c.Source == nil {
		return Client{}, ErrRuntimeConfigUnavailable
	}
	config, err := c.Source.GetQBittorrentRuntimeConfig(ctx)
	if err != nil {
		return Client{}, fmt.Errorf("%w: %w", ErrRuntimeConfigUnavailable, err)
	}
	location := NewNamespaceLocation(config)
	if location.Endpoint == "" || location.Digest() != namespace {
		return Client{}, ErrPathSnapshotChanged
	}
	current, err := DerivePathSnapshot(config)
	if err != nil || !PathSnapshotsEqual(current, snapshot) {
		return Client{}, ErrPathSnapshotChanged
	}
	return Client{Endpoint: config.Endpoint, Username: config.Username, Password: config.Password, Category: config.Category,
		UserTags: config.UserTags, SaveRoot: snapshot.RemoteRoot, ApplicationRoot: snapshot.ApplicationRoot, HTTP: c.HTTP, Timeout: c.Timeout}, nil
}

func (c ConfiguredClient) ObservePartialDownload(ctx context.Context, request PartialDownloadRequest) (domain.ProviderTask, error) {
	client, err := c.partialClient(ctx, request.Snapshot, request.Namespace)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	return client.ObservePartialDownload(ctx, request)
}

type partialFile struct {
	Name     string   `json:"name"`
	Index    *int     `json:"index"`
	Priority *int     `json:"priority"`
	Size     *int64   `json:"size"`
	Progress *float64 `json:"progress"`
}

type partialFilePlan struct {
	enable, disable []string
	selected        []partialFile
}

func planPartialPriorities(selection domain.PartialDownload, manifest map[string]int64, files []partialFile) (partialFilePlan, error) {
	if len(files) != selection.FileCount || len(manifest) != selection.FileCount {
		return partialFilePlan{}, ErrPartialDownloadIdentity
	}
	wanted := make(map[string]bool, len(selection.Paths))
	for _, path := range selection.Paths {
		wanted[path] = true
	}
	indexes, names := map[int]bool{}, map[string]bool{}
	var plan partialFilePlan
	for _, file := range files {
		size, known := manifest[file.Name]
		if !known || file.Index == nil || *file.Index < 0 || *file.Index >= len(files) || indexes[*file.Index] || names[file.Name] ||
			!fs.ValidPath(file.Name) || strings.Contains(file.Name, "\\") || file.Priority == nil || *file.Priority < 0 ||
			file.Size == nil || *file.Size != size || size < 0 || file.Progress == nil || *file.Progress < 0 || *file.Progress > 1 {
			return partialFilePlan{}, ErrPartialDownloadIdentity
		}
		indexes[*file.Index], names[file.Name] = true, true
		id := strconv.Itoa(*file.Index)
		if wanted[file.Name] {
			plan.selected = append(plan.selected, file)
			if *file.Priority == 0 {
				plan.enable = append(plan.enable, id)
			}
		} else if *file.Priority != 0 {
			plan.disable = append(plan.disable, id)
		}
	}
	if len(plan.selected) != len(wanted) {
		return partialFilePlan{}, ErrPartialDownloadIdentity
	}
	return plan, nil
}

func (c Client) ObservePartialDownload(ctx context.Context, request PartialDownloadRequest) (domain.ProviderTask, error) {
	var empty domain.ProviderTask
	tag, ok := strings.CutPrefix(request.TaskID, "tag:")
	if !ok || !strings.HasPrefix(tag, "bansoko-") || request.Selection.Validate() != nil {
		return empty, ErrPartialDownloadIdentity
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return empty, err
	}
	lookup := func() (TorrentInfo, error) {
		item, found, err := c.findByTag(ctx, client, tag)
		if err != nil {
			return item, err
		}
		if !found {
			return item, domain.ErrProviderTaskAbsent
		}
		if !matchesPartialTask(item, tag, request.Hashes, request.Snapshot) {
			return item, ErrPartialDownloadIdentity
		}
		return item, nil
	}
	readPlan := func(hash string) (partialFilePlan, error) {
		var files []partialFile
		uri := "/api/v2/torrents/files?hash=" + url.QueryEscape(hash)
		if err := c.partialRequest(ctx, client, http.MethodGet, uri, nil, &files); err != nil {
			return partialFilePlan{}, err
		}
		if len(files) == 0 {
			return partialFilePlan{}, ErrPartialDownloadPending
		}
		return planPartialPriorities(request.Selection, request.Manifest, files)
	}
	item, err := lookup()
	if err != nil {
		return empty, err
	}
	plan, err := readPlan(item.Hash)
	if err != nil {
		return empty, err
	}
	stopped := func(info TorrentInfo) bool {
		state := normalizeProviderState(info.State)
		return state == "pauseddl" || state == "stoppeddl" || state == "pausedup" || state == "stoppedup"
	}
	if !request.StartSent {
		switch normalizeProviderState(item.State) {
		case "checkingresumedata", "checkingdl", "checkingup":
			return empty, ErrPartialDownloadPending
		}
		if !stopped(item) || request.BeforeWrite == nil {
			return empty, ErrPartialDownloadIdentity
		}
		write := func(uri string, form url.Values, start bool) error {
			fresh, err := lookup()
			if err != nil {
				return err
			}
			if fresh.Hash != item.Hash || !stopped(fresh) {
				return ErrPartialDownloadIdentity
			}
			remaining, err := request.BeforeWrite(ctx, start)
			if err != nil {
				return err
			}
			if remaining <= 0 {
				return context.DeadlineExceeded
			}
			callCtx, cancel := context.WithTimeout(ctx, remaining)
			defer cancel()
			return c.partialRequest(callCtx, client, http.MethodPost, uri, form, nil)
		}
		for _, batch := range []struct {
			ids      []string
			priority string
		}{{plan.enable, "1"}, {plan.disable, "0"}} {
			for i := 0; i < len(batch.ids); i += 128 {
				form := url.Values{"hash": {item.Hash}, "id": {strings.Join(batch.ids[i:min(i+128, len(batch.ids))], "|")}, "priority": {batch.priority}}
				if err := write("/api/v2/torrents/filePrio", form, false); err != nil {
					return empty, err
				}
			}
		}
		plan, err = readPlan(item.Hash)
		if err != nil {
			return empty, err
		}
		if len(plan.enable)+len(plan.disable) != 0 {
			return empty, ErrPartialDownloadIdentity
		}
		version, err := c.appVersion(ctx, client)
		if err != nil {
			return empty, err
		}
		field, err := initialStopField(version)
		if err != nil {
			return empty, err
		}
		action := "start"
		if field == "paused" {
			action = "resume"
		}
		if err := write("/api/v2/torrents/"+action, url.Values{"hashes": {item.Hash}}, true); err != nil {
			return empty, err
		}
		item, err = lookup()
		if err != nil {
			return empty, err
		}
		plan, err = readPlan(item.Hash)
		if err != nil {
			return empty, err
		}
	}
	if len(plan.enable)+len(plan.disable) != 0 {
		return empty, ErrPartialDownloadIdentity
	}
	task := c.buildProviderTask(request.TaskID, item, nil, &request.Snapshot)
	var complete, total int64
	allComplete := true
	for _, file := range plan.selected {
		total += *file.Size
		complete += int64(float64(*file.Size) * *file.Progress)
		if *file.Progress != 1 && *file.Size != 0 {
			allComplete = false
			continue
		}
		path := filepath.Join(item.SavePath, filepath.FromSlash(file.Name))
		task.Paths = append(task.Paths, path)
		task.Files = append(task.Files, domain.ProviderFile{Path: path, SizeBytes: *file.Size, MediaKind: artifactMediaKind(path), Language: subtitleLanguage(path), Progress: 1})
	}
	task.Path = primaryArtifactPath(task.Paths)
	task.ContentCompleteBytes, task.PayloadTotalBytes = &complete, &total
	if total > 0 {
		task.Progress = float64(complete) / float64(total)
	}
	if task.State == domain.TaskCompleted && !allComplete {
		return empty, ErrPartialDownloadIdentity
	}
	return task, nil
}

func (c Client) partialRequest(ctx context.Context, client *http.Client, method, uri string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Endpoint, "/")+uri, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	direct := *client
	direct.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := doRequest(&direct, req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("qBittorrent partial-download request failed with HTTP %d", response.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(response.Body).Decode(out)
	}
	return nil
}
