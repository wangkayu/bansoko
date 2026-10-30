package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/download/domain"
)

type Client struct {
	Endpoint        string
	Username        string
	Password        string
	Category        string
	UserTags        []string
	SaveRoot        string
	ApplicationRoot string
	HTTP            *http.Client
	Timeout         time.Duration
}

type PathSnapshot = domain.PathSnapshot

// ErrPathSnapshotChanged means the current qBittorrent path facts no longer
// match the facts frozen for an existing external effect.
var ErrPathSnapshotChanged = errors.New("qBittorrent path snapshot changed")

var ErrLookupAmbiguous = errors.New("multiple qBittorrent tasks match frozen admission facts")

// ErrHashOccupied means qBittorrent already holds a task with the same info
// hash that does not carry this submission's operation tag. qBittorrent keeps
// one task per info hash, so adding the torrent again cannot succeed; the
// submission is refused instead of being sent (or, when the add was already
// sent and refused, the refusal is attributed to that task).
var ErrHashOccupied = errors.New("the download client already has a task for this torrent")

// NormalizeEvidencePath canonicalizes one provider-reported content path with
// the same rules as the frozen snapshot roots: no parent-traversal segments,
// no control characters, absolute only. An empty input stays representable as
// empty so callers can distinguish "no evidence" from "unsafe evidence".
func NormalizeEvidencePath(value string) (string, error) {
	return normalizeSnapshotPath(value, true)
}

// NormalizePathSnapshot canonicalizes and bounds the credential-free path
// facts persisted with a DownloadTask. It intentionally does not include the
// qB endpoint, username, password, tags, or connection owner. Under the
// same-path convention the application root defaults to the qB-reported root.
func NormalizePathSnapshot(snapshot domain.PathSnapshot) (domain.PathSnapshot, error) {
	remoteRoot, err := normalizeSnapshotPath(snapshot.RemoteRoot, true)
	if err != nil {
		return domain.PathSnapshot{}, fmt.Errorf("normalize qB remote root: %w", err)
	}
	applicationRoot, err := normalizeSnapshotPath(snapshot.ApplicationRoot, true)
	if err != nil {
		return domain.PathSnapshot{}, fmt.Errorf("normalize qB application root: %w", err)
	}
	if applicationRoot == "" {
		applicationRoot = remoteRoot
	}
	return domain.PathSnapshot{
		RemoteRoot:      remoteRoot,
		ApplicationRoot: applicationRoot,
	}, nil
}

// DerivePathSnapshot resolves the application-visible root from the current
// typed qB runtime config. A missing SaveRoot remains representable for legacy
// direct-client fixtures; production DownloadTask submission still requires it.
// ApplicationRoot is optional: when empty NormalizePathSnapshot applies the
// same-path convention (application root defaults to the qB-reported root).
func DerivePathSnapshot(config RuntimeConfig) (domain.PathSnapshot, error) {
	return NormalizePathSnapshot(domain.PathSnapshot{
		RemoteRoot:      config.SaveRoot,
		ApplicationRoot: config.ApplicationRoot,
	})
}

func PathSnapshotsEqual(left, right domain.PathSnapshot) bool {
	leftNormalized, leftErr := NormalizePathSnapshot(left)
	rightNormalized, rightErr := NormalizePathSnapshot(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftRaw, _ := json.Marshal(leftNormalized)
	rightRaw, _ := json.Marshal(rightNormalized)
	return string(leftRaw) == string(rightRaw)
}

func (c Client) pathSnapshot() (*domain.PathSnapshot, error) {
	snapshot, err := NormalizePathSnapshot(domain.PathSnapshot{
		RemoteRoot:      c.SaveRoot,
		ApplicationRoot: c.ApplicationRoot,
	})
	if err != nil {
		return nil, err
	}
	if snapshot.RemoteRoot == "" && snapshot.ApplicationRoot == "" {
		return nil, nil
	}
	return &snapshot, nil
}

func (c Client) withSnapshot(snapshot domain.PathSnapshot) (Client, error) {
	normalized, err := NormalizePathSnapshot(snapshot)
	if err != nil {
		return Client{}, err
	}
	c.SaveRoot = normalized.RemoteRoot
	c.ApplicationRoot = normalized.ApplicationRoot
	return c, nil
}

func normalizeSnapshotPath(value string, allowEmpty bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("path is required")
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("path contains invalid control characters")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", errors.New("path cannot contain parent traversal")
		}
	}
	cleaned := filepath.Clean(filepath.FromSlash(value))
	if cleaned == "." || len(cleaned) > 4096 || !filepath.IsAbs(cleaned) {
		return "", errors.New("path must be an absolute path of valid length")
	}
	return cleaned, nil
}

// RuntimeConfig is the provider-facing qBittorrent projection. The source is
// resolved by the composition root from the typed configuration owner.
type RuntimeConfig struct {
	// ConnectionID is an internal owner reference. It is never exposed in the
	// public qBittorrent connection DTO or in provider credentials.
	ConnectionID string
	// ConnectionVersion is the row version of that connection, so reconfiguring
	// it names a new provider namespace instead of silently reusing the old one.
	ConnectionVersion int64
	Endpoint          string
	Username          string
	Password          string
	Category          string
	UserTags          []string
	SaveRoot          string
	// ApplicationRoot is the optional local filesystem root mapped from
	// SaveRoot for mixed deployments. Empty means the same-path convention.
	ApplicationRoot string
}

type RuntimeConfigSource interface {
	GetQBittorrentRuntimeConfig(context.Context) (RuntimeConfig, error)
}

// FileSubmitter is the narrow provider capability for uploading a program-owned
// torrent payload. Production download effects do not accept URL or magnet input.
type FileSubmitter interface {
	SubmitFile(context.Context, []byte, string, string, string) (domain.ProviderTask, error)
}

// SnapshotFileSubmitter is an optional file-upload capability that submits with
// path facts frozen on the DownloadTask.
type SnapshotFileSubmitter interface {
	SubmitFileWithSnapshot(context.Context, []byte, string, domain.PathSnapshot, string) (domain.ProviderTask, error)
}

// SnapshotLookup is an optional provider capability for resolving stable qB
// evidence with the frozen path facts.
type SnapshotLookup interface {
	LookupByOperationKeyWithSnapshot(context.Context, string, domain.PathSnapshot) (domain.ProviderTask, bool, error)
}

// SnapshotHashLookup is an optional recovery capability. Hashes come from the
// program-owned torrent parser, so recovery does not ask qB to reinterpret a
// v2 torrent or derive a different hash identity.
type SnapshotHashLookup interface {
	LookupByInfoHashesWithSnapshot(context.Context, []string, domain.PathSnapshot) (domain.ProviderTask, bool, error)
}

// SnapshotSyncer is an optional provider capability for syncing qB evidence
// without resolving paths through newer runtime configuration.
type SnapshotSyncer interface {
	SyncWithSnapshot(context.Context, string, domain.PathSnapshot) (domain.ProviderTask, error)
}

// OperationTagCleaner is an optional provider capability used when a stable
// WorkItem operation key is the surviving identity of an absent qB task.
type OperationTagCleaner interface {
	CleanupOperationTag(context.Context, string) error
}

// SnapshotProvider combines the optional frozen-snapshot capabilities. The
// individual interfaces remain useful for narrow fakes and partial adapters.
type SnapshotProvider interface {
	SnapshotFileSubmitter
	SnapshotLookup
	SnapshotSyncer
}

// ErrRuntimeConfigUnavailable marks a provider operation that could not resolve
// the typed qBittorrent connection projection. It is an internal classification
// signal; public connection DTOs remain unchanged.
var ErrRuntimeConfigUnavailable = errors.New("qBittorrent runtime configuration is unavailable")

// ConfiguredClient resolves provider settings at operation time so endpoint
// and credentials cannot drift from the typed connection after
// a settings update. It intentionally keeps the same Provider and Canceler
// surface as Client.
type ConfiguredClient struct {
	Source  RuntimeConfigSource
	HTTP    *http.Client
	Timeout time.Duration
}

func (c ConfiguredClient) client(ctx context.Context) (Client, error) {
	if c.Source == nil {
		return Client{}, fmt.Errorf("%w: source is nil", ErrRuntimeConfigUnavailable)
	}
	config, err := c.Source.GetQBittorrentRuntimeConfig(ctx)
	if err != nil {
		return Client{}, fmt.Errorf("%w: %w", ErrRuntimeConfigUnavailable, err)
	}
	client := Client{Endpoint: config.Endpoint, Username: config.Username, Password: config.Password,
		Category: config.Category, UserTags: append([]string(nil), config.UserTags...), SaveRoot: config.SaveRoot,
		HTTP: c.HTTP, Timeout: c.Timeout}
	if snapshot, snapshotErr := DerivePathSnapshot(config); snapshotErr == nil {
		client.ApplicationRoot = snapshot.ApplicationRoot
	}
	return client, nil
}

func (c ConfiguredClient) clientForSnapshot(ctx context.Context, snapshot domain.PathSnapshot) (Client, error) {
	client, err := c.client(ctx)
	if err != nil {
		return Client{}, err
	}
	current, err := client.pathSnapshot()
	if err != nil {
		return Client{}, fmt.Errorf("derive current qB path snapshot: %w", err)
	}
	if current == nil || !PathSnapshotsEqual(*current, snapshot) {
		return Client{}, ErrPathSnapshotChanged
	}
	return client.withSnapshot(snapshot)
}

// SubmitFile resolves the current typed qB configuration before uploading a
// torrent file. It preserves lookup-before-retry semantics through the stable
// idempotency tag used by Client.SubmitFile.
func (c ConfiguredClient) SubmitFile(ctx context.Context, payload []byte, filename, savePath, idempotencyKey string) (domain.ProviderTask, error) {
	client, err := c.client(ctx)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	if strings.TrimSpace(client.SaveRoot) != "" && filepath.Clean(savePath) != filepath.Clean(client.SaveRoot) {
		return domain.ProviderTask{}, errors.New("qBittorrent save root changed while submitting the download")
	}
	return client.SubmitFile(ctx, payload, filename, savePath, idempotencyKey)
}

// SubmitFileWithSnapshot resolves current credentials only after ensuring the
// frozen path facts remain current, then uploads into the frozen remote root.
func (c ConfiguredClient) SubmitFileWithSnapshot(ctx context.Context, payload []byte, filename string, snapshot domain.PathSnapshot, idempotencyKey string) (domain.ProviderTask, error) {
	client, err := c.clientForSnapshot(ctx, snapshot)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	return client.SubmitFileWithSnapshot(ctx, payload, filename, snapshot, idempotencyKey)
}

func (c ConfiguredClient) LookupByOperationKey(ctx context.Context, operationKey string) (domain.ProviderTask, bool, error) {
	client, err := c.client(ctx)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	return client.LookupByOperationKey(ctx, operationKey)
}

// LookupByOperationKeyWithSnapshot resolves qB evidence through the frozen
// mapping so a later connection edit cannot remap returned artifact paths.
func (c ConfiguredClient) LookupByOperationKeyWithSnapshot(ctx context.Context, operationKey string, snapshot domain.PathSnapshot) (domain.ProviderTask, bool, error) {
	client, err := c.clientForSnapshot(ctx, snapshot)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	return client.LookupByOperationKeyWithSnapshot(ctx, operationKey, snapshot)
}

// LookupByInfoHashesWithSnapshot resolves provider evidence using hashes parsed
// and frozen by the application. The client rejects ambiguous matches.
func (c ConfiguredClient) LookupByInfoHashesWithSnapshot(ctx context.Context, hashes []string, snapshot domain.PathSnapshot) (domain.ProviderTask, bool, error) {
	client, err := c.clientForSnapshot(ctx, snapshot)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	return client.LookupByInfoHashesWithSnapshot(ctx, hashes, snapshot)
}

func (c Client) LookupByOperationKey(ctx context.Context, operationKey string) (domain.ProviderTask, bool, error) {
	return c.lookupByOperationKey(ctx, operationKey, nil)
}

func (c Client) LookupByOperationKeyWithSnapshot(ctx context.Context, operationKey string, snapshot domain.PathSnapshot) (domain.ProviderTask, bool, error) {
	normalized, err := NormalizePathSnapshot(snapshot)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	client, err := c.withSnapshot(normalized)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	return client.lookupByOperationKey(ctx, operationKey, &normalized)
}

// LookupByInfoHashesWithSnapshot resolves provider evidence using hashes parsed
// and frozen by the application. The client rejects ambiguous matches.
func (c Client) LookupByInfoHashesWithSnapshot(ctx context.Context, hashes []string, snapshot domain.PathSnapshot) (domain.ProviderTask, bool, error) {
	normalized, err := NormalizePathSnapshot(snapshot)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	client, err := c.withSnapshot(normalized)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	return client.lookupByInfoHashes(ctx, hashes, &normalized)
}

func (c Client) lookupByOperationKey(ctx context.Context, operationKey string, snapshot *domain.PathSnapshot) (domain.ProviderTask, bool, error) {
	operationKey = strings.TrimSpace(operationKey)
	if operationKey == "" {
		return domain.ProviderTask{}, false, errors.New("qB operation key is required")
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return domain.ProviderTask{}, false, err
	}
	item, found, err := c.findByTag(ctx, client, "bansoko-"+operationKey)
	if err != nil {
		return domain.ProviderTask{}, false, fmt.Errorf("look up qB operation key: %w", err)
	}
	if !found {
		return domain.ProviderTask{}, false, nil
	}
	return c.providerTaskWithSnapshot(ctx, client, "tag:bansoko-"+operationKey, item, snapshot), true, nil
}

func (c Client) lookupByInfoHashes(ctx context.Context, hashes []string, snapshot *domain.PathSnapshot) (domain.ProviderTask, bool, error) {
	candidates := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, value := range hashes {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		candidates = append(candidates, value)
	}
	if len(candidates) == 0 {
		return domain.ProviderTask{}, false, nil
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return domain.ProviderTask{}, false, err
	}
	item, found, err := c.findTorrents(ctx, client, "hashes="+url.QueryEscape(strings.Join(candidates, "|")))
	if err != nil {
		return domain.ProviderTask{}, false, fmt.Errorf("look up qB torrent hashes: %w", err)
	}
	if !found {
		return domain.ProviderTask{}, false, nil
	}
	task, err := c.providerAdmissionTask(ctx, client, "hash:"+item.Hash, item, snapshot)
	if err != nil {
		return domain.ProviderTask{}, false, err
	}
	return task, true, nil
}

func (c ConfiguredClient) ClassifyProviderError(err error) domain.ProviderErrorClass {
	return classifyProviderError(err)
}

func (c Client) ClassifyProviderError(err error) domain.ProviderErrorClass {
	return classifyProviderError(err)
}

func classifyProviderError(err error) domain.ProviderErrorClass {
	if err == nil {
		return domain.ProviderErrorUnknown
	}
	if errors.Is(err, ErrPathSnapshotChanged) || errors.Is(err, ErrAuthFailed) {
		return domain.ProviderErrorRetryable
	}
	if errors.Is(err, ErrHashOccupied) {
		return domain.ProviderErrorTerminal
	}
	message := strings.ToLower(err.Error())
	for _, terminal := range []string{"invalid", "required", "unsupported", "authentication", "credentials", "multiple qbittorrent tasks", "http 400", "http 401", "http 403"} {
		if strings.Contains(message, terminal) {
			return domain.ProviderErrorTerminal
		}
	}
	for _, retryable := range []string{"timeout", "deadline exceeded", "connection reset", "temporarily", "http 408", "http 429", "http 500", "http 502", "http 503", "http 504"} {
		if strings.Contains(message, retryable) {
			return domain.ProviderErrorRetryable
		}
	}
	return domain.ProviderErrorAmbiguous
}

func (c ConfiguredClient) Sync(ctx context.Context, providerTaskID string) (domain.ProviderTask, error) {
	client, err := c.client(ctx)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	return client.Sync(ctx, providerTaskID)
}

// SyncWithSnapshot resolves qB paths through the frozen mapping and rejects a
// changed current connection before any provider lookup is issued.
func (c ConfiguredClient) SyncWithSnapshot(ctx context.Context, providerTaskID string, snapshot domain.PathSnapshot) (domain.ProviderTask, error) {
	client, err := c.clientForSnapshot(ctx, snapshot)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	return client.SyncWithSnapshot(ctx, providerTaskID, snapshot)
}

// TorrentByHash resolves the current typed qB configuration and reads one task
// by infohash.
func (c ConfiguredClient) TorrentByHash(ctx context.Context, hash string) (TorrentFacts, bool, error) {
	client, err := c.client(ctx)
	if err != nil {
		return TorrentFacts{}, false, err
	}
	return client.TorrentByHash(ctx, hash)
}

// ListTorrents lists every task with the current typed qB configuration.
func (c ConfiguredClient) ListTorrents(ctx context.Context) ([]TorrentInfo, error) {
	client, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.ListTorrents(ctx)
}

// GetTorrentFiles lists one task's files with the current typed qB configuration.
func (c ConfiguredClient) GetTorrentFiles(ctx context.Context, hash string) ([]TorrentFile, error) {
	client, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.GetTorrentFiles(ctx, hash)
}

// PauseTorrent pauses one task by infohash with the current typed qB configuration.
func (c ConfiguredClient) PauseTorrent(ctx context.Context, hash string) error {
	client, err := c.client(ctx)
	if err != nil {
		return err
	}
	return client.PauseTorrent(ctx, hash)
}

// DeleteTorrent removes one task by infohash, and its files when deleteFiles is
// true, with the current typed qB configuration.
func (c ConfiguredClient) DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	client, err := c.client(ctx)
	if err != nil {
		return err
	}
	return client.DeleteTorrent(ctx, hash, deleteFiles)
}

// WebAPIVersion reads the qBittorrent WebAPI version with the current typed
// qB configuration.
func (c ConfiguredClient) WebAPIVersion(ctx context.Context) (string, error) {
	client, err := c.client(ctx)
	if err != nil {
		return "", err
	}
	return client.WebAPIVersion(ctx)
}

// SetShareLimits writes one task's share limits by infohash with the current
// typed qB configuration.
func (c ConfiguredClient) SetShareLimits(ctx context.Context, hash string, limits ShareLimits) error {
	client, err := c.client(ctx)
	if err != nil {
		return err
	}
	return client.SetShareLimits(ctx, hash, limits)
}

// CleanupOperationTag removes the exact system tag of one known operation when
// no torrent references it. It never derives candidates from a torrent hash.
func (c ConfiguredClient) CleanupOperationTag(ctx context.Context, operationKey string) error {
	client, err := c.client(ctx)
	if err != nil {
		return err
	}
	return client.CleanupOperationTag(ctx, operationKey)
}

// CleanupTaskTag removes the exact system tag carried by a saved provider
// reference. Hash-only references have no operation-tag candidate and are a
// deliberate no-op.
func (c ConfiguredClient) CleanupTaskTag(ctx context.Context, providerTaskID string) error {
	return c.CleanupTaskTags(ctx, []string{providerTaskID})
}

// CleanupTaskTags removes the exact system tags carried by saved provider
// references in one reference-check/delete batch.
func (c ConfiguredClient) CleanupTaskTags(ctx context.Context, providerTaskIDs []string) error {
	client, err := c.client(ctx)
	if err != nil {
		return err
	}
	return client.CleanupTaskTags(ctx, providerTaskIDs)
}

// CleanupTags removes only known, unreferenced bansoko system tags.
func (c ConfiguredClient) CleanupTags(ctx context.Context, candidates []string) error {
	client, err := c.client(ctx)
	if err != nil {
		return err
	}
	return client.CleanupTags(ctx, candidates)
}

func (c ConfiguredClient) Cancel(ctx context.Context, providerTaskID string) error {
	client, err := c.client(ctx)
	if err != nil {
		return err
	}
	return client.Cancel(ctx, providerTaskID)
}

// SubmitFile uploads a program-owned torrent payload as multipart/form-data in
// the torrents part. The production client has no URL or magnet add surface.
func (c Client) SubmitFile(ctx context.Context, payload []byte, filename, savePath, idempotencyKey string) (domain.ProviderTask, error) {
	return c.submitFile(ctx, payload, filename, savePath, idempotencyKey, nil)
}

// SubmitFileWithSnapshot uploads into the frozen remote root and attaches the
// normalized path snapshot to either a new submission or an existing tag match.
func (c Client) SubmitFileWithSnapshot(ctx context.Context, payload []byte, filename string, snapshot domain.PathSnapshot, idempotencyKey string) (domain.ProviderTask, error) {
	normalized, err := NormalizePathSnapshot(snapshot)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	if normalized.RemoteRoot == "" {
		return domain.ProviderTask{}, errors.New("qB path snapshot remote root is required")
	}
	client, err := c.withSnapshot(normalized)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	return client.submitFile(ctx, payload, filename, normalized.RemoteRoot, idempotencyKey, &normalized)
}

func (c Client) submitFile(ctx context.Context, payload []byte, filename, savePath, idempotencyKey string, snapshot *domain.PathSnapshot) (domain.ProviderTask, error) {
	if err := validateFileSubmitInput(c.Endpoint, payload, filename, savePath, idempotencyKey); err != nil {
		return domain.ProviderTask{}, err
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return domain.ProviderTask{}, err
	}
	tag := "bansoko-" + idempotencyKey
	if item, found, err := c.findByTag(ctx, client, tag); err != nil {
		return domain.ProviderTask{}, fmt.Errorf("look up qB idempotency tag: %w", err)
	} else if found {
		return c.providerTaskWithSnapshot(ctx, client, "tag:"+tag, item, snapshot), nil
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("torrents", filename)
	if err != nil {
		return domain.ProviderTask{}, fmt.Errorf("create qB torrent form file: %w", err)
	}
	if _, err := part.Write(payload); err != nil {
		return domain.ProviderTask{}, fmt.Errorf("write qB torrent form file: %w", err)
	}
	if err := writer.WriteField("savepath", filepath.Clean(savePath)); err != nil {
		return domain.ProviderTask{}, fmt.Errorf("write qB save path field: %w", err)
	}
	if strings.TrimSpace(c.Category) != "" {
		if err := writer.WriteField("category", c.Category); err != nil {
			return domain.ProviderTask{}, fmt.Errorf("write qB category field: %w", err)
		}
	}
	if err := writer.WriteField("tags", strings.Join(append([]string{tag}, c.UserTags...), ",")); err != nil {
		return domain.ProviderTask{}, fmt.Errorf("write qB tags field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return domain.ProviderTask{}, fmt.Errorf("close qB torrent form: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoint, "/")+"/api/v2/torrents/add", &body)
	if err != nil {
		return domain.ProviderTask{}, fmt.Errorf("create qB add request: %w", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := doRequest(client, request)
	if err != nil {
		return domain.ProviderTask{}, fmt.Errorf("submit qB task: %w", err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 || strings.TrimSpace(string(responseBody)) != "Ok." {
		return domain.ProviderTask{}, fmt.Errorf("qB task submission failed with HTTP %d", response.StatusCode)
	}
	result := domain.ProviderTask{ID: "tag:" + tag, State: domain.TaskSubmitted}
	if snapshot != nil {
		frozen := *snapshot
		result.PathSnapshot = &frozen
	}
	return result, nil
}

func validateFileSubmitInput(endpoint string, payload []byte, filename, savePath, idempotencyKey string) error {
	if strings.TrimSpace(endpoint) == "" || len(payload) == 0 || strings.TrimSpace(filename) == "" || strings.TrimSpace(savePath) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return errors.New("qBittorrent endpoint, torrent payload, filename, save path and idempotency key are required")
	}
	if filename == "." || filename == ".." || strings.ContainsAny(filename, "/\\\r\n\x00") {
		return errors.New("qBittorrent torrent filename must be a single safe path component")
	}
	return nil
}

func (c Client) Sync(ctx context.Context, providerTaskID string) (domain.ProviderTask, error) {
	return c.sync(ctx, providerTaskID, nil)
}

func (c Client) SyncWithSnapshot(ctx context.Context, providerTaskID string, snapshot domain.PathSnapshot) (domain.ProviderTask, error) {
	normalized, err := NormalizePathSnapshot(snapshot)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	client, err := c.withSnapshot(normalized)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	return client.sync(ctx, providerTaskID, &normalized)
}

func (c Client) sync(ctx context.Context, providerTaskID string, snapshot *domain.PathSnapshot) (domain.ProviderTask, error) {
	if providerTaskID == "" {
		return domain.ProviderTask{}, errors.New("qB provider task ID is required")
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return domain.ProviderTask{}, err
	}
	item, found, err := c.findByProviderTaskID(ctx, client, providerTaskID)
	if err != nil {
		return domain.ProviderTask{}, fmt.Errorf("look up qB task: %w", err)
	}
	if !found {
		// A successful lookup reply without the task is the definitive absence
		// answer; every lookup failure above stays an ordinary error.
		return domain.ProviderTask{}, fmt.Errorf("qB task %q was not found: %w", providerTaskID, domain.ErrProviderTaskAbsent)
	}
	return c.providerTaskWithSnapshot(ctx, client, providerTaskID, item, snapshot), nil
}

// providerAdmissionTask builds the provider task for the cross-owner hash
// fallback. Unlike the replay/sync paths it fails closed: a files-read outage
// must surface as a typed provider error instead of collapsing into a task
// with no content evidence.
func (c Client) providerAdmissionTask(ctx context.Context, client *http.Client, providerTaskID string, item TorrentInfo, snapshot *domain.PathSnapshot) (domain.ProviderTask, error) {
	files, err := c.artifactFiles(ctx, client, item)
	if err != nil {
		return domain.ProviderTask{}, fmt.Errorf("read qB torrent content evidence: %w", err)
	}
	return c.buildProviderTask(providerTaskID, item, files, snapshot), nil
}

func (c Client) providerTaskWithSnapshot(ctx context.Context, client *http.Client, providerTaskID string, item TorrentInfo, snapshot *domain.PathSnapshot) domain.ProviderTask {
	// Operation-key replay, sync and submit-replay tolerate a transient
	// files-read outage: those paths keep their independent rules and only
	// lose the file listing for that observation.
	files, _ := c.artifactFiles(ctx, client, item)
	return c.buildProviderTask(providerTaskID, item, files, snapshot)
}

func (c Client) buildProviderTask(providerTaskID string, item TorrentInfo, files []domain.ProviderFile, snapshot *domain.PathSnapshot) domain.ProviderTask {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	path := primaryArtifactPath(paths)
	result := domain.ProviderTask{ID: providerTaskID, Hash: item.Hash, Name: item.Name, SavePath: item.SavePath,
		State: mapState(item.State), ProviderState: normalizeProviderState(item.State),
		Path: path, Paths: paths, Files: files, ContentPath: strings.TrimSpace(item.ContentPath),
		Progress: clampProgress(item.Progress), Error: item.TrackerStatus}
	if item.Dlspeed >= 0 {
		speed := item.Dlspeed
		result.DownloadSpeed = &speed
	}
	// The integer observation is presence-aware at the protocol boundary: qB always
	// reports both fields, so a decoded zero is a reported zero and never an
	// absence. qB derives the two counts separately, though, so one read can pair a
	// content-complete count with a smaller total; complete bytes above the total
	// are provider evidence corruption rather than progress, and such a read
	// carries no trustworthy mark at all. An observation with neither field is the
	// presence-aware form of that fact: the owner-held judgment leaves the stall
	// clock and the verified high-water mark untouched for it, so the provider's
	// incoherent pair is dropped here and the next poll re-observes instead of
	// freezing corruption into the task.
	if item.Completed >= 0 && item.Size >= 0 && item.Completed <= item.Size {
		contentComplete, payloadTotal := item.Completed, item.Size
		result.ContentCompleteBytes = &contentComplete
		result.PayloadTotalBytes = &payloadTotal
	}
	if snapshot == nil {
		if current, snapshotErr := c.pathSnapshot(); snapshotErr == nil {
			snapshot = current
		}
	}
	if snapshot != nil {
		frozen := *snapshot
		result.PathSnapshot = &frozen
	}
	return result
}

// Health authenticates and reads the qB application version. It is used by
// explicit connection tests and never by ordinary read requests.
func (c Client) Health(ctx context.Context) (string, error) {
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/api/v2/app/version", nil)
	if err != nil {
		return "", fmt.Errorf("create qB health request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return "", fmt.Errorf("check qB health: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("qB health failed with HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 256))
	if err != nil {
		return "", fmt.Errorf("read qB version: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

func (c Client) Cancel(ctx context.Context, providerTaskID string) error {
	if providerTaskID == "" {
		return errors.New("qB provider task ID is required")
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return err
	}
	hash := strings.TrimPrefix(providerTaskID, "hash:")
	if strings.HasPrefix(providerTaskID, "tag:") {
		item, found, err := c.findByTag(ctx, client, strings.TrimPrefix(providerTaskID, "tag:"))
		if err != nil {
			return fmt.Errorf("look up qB task for cancellation: %w", err)
		}
		if !found {
			// A missing torrent is already in the requested terminal state. This
			// makes replay after a provider accepted delete idempotent and does not
			// infer a hash from the correlation tag. The saved tag is still an exact
			// cleanup candidate, covering a crash after delete but before cleanup.
			if err := c.CleanupTaskTag(ctx, providerTaskID); err != nil {
				return fmt.Errorf("clean up absent cancelled qB task tag: %w", err)
			}
			return nil
		}
		hash = item.Hash
	}
	if strings.TrimSpace(hash) == "" {
		return errors.New("qB provider task has no torrent hash")
	}
	form := url.Values{"hashes": {hash}, "deleteFiles": {"false"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoint, "/")+"/api/v2/torrents/delete", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create qB cancel request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := doRequest(client, request)
	if err != nil {
		return fmt.Errorf("cancel qB task: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("qB cancel failed with HTTP %d", response.StatusCode)
	}
	if err := c.confirmTorrentDeleted(ctx, client, hash); err != nil {
		return err
	}
	if err := c.CleanupTaskTag(ctx, providerTaskID); err != nil {
		return fmt.Errorf("clean up cancelled qB task tag: %w", err)
	}
	return nil
}

// A qBittorrent delete is applied asynchronously: the request succeeds while
// the task is still listed for a moment. Cleaning a tag before the task leaves
// the list would read the task as a live reference and keep the tag, so every
// delete that is followed by a tag cleanup first confirms the task is gone. A
// task that stays listed past the window fails the delete and lets the caller's
// retry chain converge later.
var (
	deleteVerifyWindow   = 5 * time.Second
	deleteVerifyInterval = 200 * time.Millisecond
	deleteVerifySleep    = func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
)

// ConfirmTorrentDeleted re-reads the torrent list until hash is absent, bounded
// by the delete verify window.
func (c Client) ConfirmTorrentDeleted(ctx context.Context, hash string) error {
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return err
	}
	return c.confirmTorrentDeleted(ctx, client, hash)
}

func (c Client) confirmTorrentDeleted(ctx context.Context, client *http.Client, hash string) error {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return errors.New("qB torrent hash is required to confirm a delete")
	}
	start := time.Now()
	for {
		torrents, err := c.listTorrents(ctx, client)
		if err != nil {
			return fmt.Errorf("confirm qB torrent delete: %w", err)
		}
		listed := false
		for _, torrent := range torrents {
			if strings.EqualFold(strings.TrimSpace(torrent.Hash), hash) {
				listed = true
				break
			}
		}
		if !listed {
			return nil
		}
		if deleteVerifyWindow <= 0 || time.Since(start)+deleteVerifyInterval > deleteVerifyWindow {
			return fmt.Errorf("qB task %s is still listed after delete", hash)
		}
		if err := deleteVerifySleep(ctx, deleteVerifyInterval); err != nil {
			return fmt.Errorf("confirm qB torrent delete: %w", err)
		}
	}
}

// ListTorrents returns every torrent known to the qBittorrent instance. It is
// used by the management dialog and never by the owner pipeline, which resolves
// tasks by stable operation tag or frozen hash.
func (c Client) ListTorrents(ctx context.Context) ([]TorrentInfo, error) {
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return nil, err
	}
	return c.listTorrents(ctx, client)
}

func (c Client) listTorrents(ctx context.Context, client *http.Client) ([]TorrentInfo, error) {
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/info"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create qB list request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return nil, fmt.Errorf("request qB torrent list: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("qB torrent list failed with HTTP %d", response.StatusCode)
	}
	var torrents []TorrentInfo
	if err := json.NewDecoder(response.Body).Decode(&torrents); err != nil {
		return nil, fmt.Errorf("decode qB torrent list: %w", err)
	}
	return torrents, nil
}

const systemTagPrefix = "bansoko-"

// CleanupOperationTag cleans the one exact tag generated from a stable
// operation key. Empty or delimiter-containing keys are rejected rather than
// being allowed to widen the deleteTags form into other tags.
func (c Client) CleanupOperationTag(ctx context.Context, operationKey string) error {
	operationKey = strings.TrimSpace(operationKey)
	if operationKey == "" {
		return errors.New("qB operation key is required for tag cleanup")
	}
	if strings.ContainsAny(operationKey, ",\r\n\x00") {
		return errors.New("qB operation key contains a tag delimiter")
	}
	return c.CleanupTags(ctx, []string{systemTagPrefix + operationKey})
}

// CleanupTaskTag cleans only a tag: provider reference that names a concrete
// bansoko system tag. Hash-only and foreign references carry no safe candidate.
func (c Client) CleanupTaskTag(ctx context.Context, providerTaskID string) error {
	return c.CleanupTaskTags(ctx, []string{providerTaskID})
}

// CleanupTaskTags cleans only exact tag: provider references in one batch.
// Hash-only and foreign references carry no safe candidate.
func (c Client) CleanupTaskTags(ctx context.Context, providerTaskIDs []string) error {
	candidates := make([]string, 0, len(providerTaskIDs))
	for _, providerTaskID := range providerTaskIDs {
		providerTaskID = strings.TrimSpace(providerTaskID)
		if !strings.HasPrefix(providerTaskID, "tag:") {
			continue
		}
		tag := strings.TrimSpace(strings.TrimPrefix(providerTaskID, "tag:"))
		if !strings.HasPrefix(tag, systemTagPrefix) || len(tag) == len(systemTagPrefix) {
			continue
		}
		candidates = append(candidates, tag)
	}
	return c.CleanupTags(ctx, candidates)
}

// CleanupTags deletes only exact bansoko system-tag candidates supplied by an
// owning workflow. It re-reads all torrents immediately before deleteTags and
// retains every candidate still referenced by any torrent. Configured user tags
// are excluded even for a manually constructed Client that bypassed config
// validation. There is intentionally no scan for unknown historical tags.
func (c Client) CleanupTags(ctx context.Context, candidates []string) error {
	userTags := make(map[string]struct{}, len(c.UserTags))
	for _, tag := range c.UserTags {
		userTags[strings.TrimSpace(tag)] = struct{}{}
	}
	known := make(map[string]struct{}, len(candidates))
	for _, raw := range candidates {
		tag := strings.TrimSpace(raw)
		if !strings.HasPrefix(tag, systemTagPrefix) || len(tag) == len(systemTagPrefix) {
			continue
		}
		if strings.ContainsAny(tag, ",\r\n\x00") {
			return fmt.Errorf("qB cleanup candidate contains a tag delimiter")
		}
		if _, configured := userTags[tag]; configured {
			continue
		}
		known[tag] = struct{}{}
	}
	if len(known) == 0 {
		return nil
	}

	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return err
	}
	torrents, err := c.listTorrents(ctx, client)
	if err != nil {
		return fmt.Errorf("recheck qB system-tag references: %w", err)
	}
	for _, torrent := range torrents {
		for _, raw := range strings.Split(torrent.Tags, ",") {
			delete(known, strings.TrimSpace(raw))
		}
	}
	if len(known) == 0 {
		return nil
	}
	unreferenced := make([]string, 0, len(known))
	for tag := range known {
		unreferenced = append(unreferenced, tag)
	}
	sort.Strings(unreferenced)
	form := url.Values{"tags": {strings.Join(unreferenced, ",")}}
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/deleteTags"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create qB delete-tags request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := doRequest(client, request)
	if err != nil {
		return fmt.Errorf("request qB delete-tags: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("qB delete-tags failed with HTTP %d", response.StatusCode)
	}
	return nil
}

// PauseTorrent pauses a single torrent by hash. qBittorrent 5 (WebAPI 2.11)
// renamed torrents/pause to torrents/stop and removed the old route, so the
// current route is tried first and the 4.x route only on HTTP 404.
func (c Client) PauseTorrent(ctx context.Context, hash string) error {
	return c.postTorrentActionCompat(ctx, "/api/v2/torrents/stop", "/api/v2/torrents/pause", hash)
}

// ResumeTorrent resumes a single paused torrent by hash (torrents/start on
// qBittorrent 5, torrents/resume on 4.x).
func (c Client) ResumeTorrent(ctx context.Context, hash string) error {
	return c.postTorrentActionCompat(ctx, "/api/v2/torrents/start", "/api/v2/torrents/resume", hash)
}

// errTorrentActionNotFound marks an action route this qB version does not
// serve (HTTP 404), the only condition that selects the legacy route.
var errTorrentActionNotFound = errors.New("qB action route not found")

func (c Client) postTorrentActionCompat(ctx context.Context, current, legacy, hash string) error {
	err := c.postTorrentAction(ctx, current, hash)
	if errors.Is(err, errTorrentActionNotFound) {
		return c.postTorrentAction(ctx, legacy, hash)
	}
	return err
}

func (c Client) postTorrentAction(ctx context.Context, path, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return errors.New("qB torrent hash is required")
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return err
	}
	form := url.Values{"hashes": {hash}}
	endpoint := strings.TrimRight(c.Endpoint, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create qB action request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := doRequest(client, request)
	if err != nil {
		return fmt.Errorf("request qB action %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: qB action %s failed with HTTP %d", errTorrentActionNotFound, path, response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("qB action %s failed with HTTP %d", path, response.StatusCode)
	}
	return nil
}

// DeleteTorrent removes a torrent from qBittorrent. When deleteFiles is true
// the downloaded data is also removed; otherwise only the task is removed.
func (c Client) DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	if strings.TrimSpace(hash) == "" {
		return errors.New("qB torrent hash is required")
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return err
	}
	form := url.Values{"hashes": {hash}, "deleteFiles": {strconv.FormatBool(deleteFiles)}}
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/delete"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create qB delete request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := doRequest(client, request)
	if err != nil {
		return fmt.Errorf("request qB delete: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("qB delete failed with HTTP %d", response.StatusCode)
	}
	return nil
}

// ShareLimits is one torrents/setShareLimits request. A limit of -1 means no
// limit and -2 the global limit. ShareLimitAction and ShareLimitsMode are sent
// only when set: a qBittorrent older than the WebAPI that introduced them
// does not know the parameters.
type ShareLimits struct {
	RatioLimit               float64
	SeedingTimeLimit         int64
	InactiveSeedingTimeLimit int64
	ShareLimitAction         string
	ShareLimitsMode          string
}

// SetShareLimits writes the share limits of one task by infohash.
// inactiveSeedingTimeLimit is always sent: qBittorrent 4.6 answers 400 when it
// is missing.
func (c Client) SetShareLimits(ctx context.Context, hash string, limits ShareLimits) error {
	if strings.TrimSpace(hash) == "" {
		return errors.New("qB torrent hash is required")
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return err
	}
	form := url.Values{
		"hashes":                   {hash},
		"ratioLimit":               {strconv.FormatFloat(limits.RatioLimit, 'f', -1, 64)},
		"seedingTimeLimit":         {strconv.FormatInt(limits.SeedingTimeLimit, 10)},
		"inactiveSeedingTimeLimit": {strconv.FormatInt(limits.InactiveSeedingTimeLimit, 10)},
	}
	if limits.ShareLimitAction != "" {
		form.Set("shareLimitAction", limits.ShareLimitAction)
	}
	if limits.ShareLimitsMode != "" {
		form.Set("shareLimitsMode", limits.ShareLimitsMode)
	}
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/setShareLimits"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create qB share limits request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := doRequest(client, request)
	if err != nil {
		return fmt.Errorf("request qB share limits: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("qB share limits failed with HTTP %d", response.StatusCode)
	}
	return nil
}

// WebAPIVersion reads app/webapiVersion (for example "2.9.3").
func (c Client) WebAPIVersion(ctx context.Context) (string, error) {
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/app/webapiVersion"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create qB WebAPI version request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return "", fmt.Errorf("request qB WebAPI version: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("qB WebAPI version failed with HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil {
		return "", fmt.Errorf("read qB WebAPI version: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

// TorrentFacts is what qBittorrent reports about one task addressed by its
// infohash: identity, where it saves, where its content lives, and the
// complete file list (including incomplete files) as qBittorrent names them,
// relative to the save path.
type TorrentFacts struct {
	Hash        string
	Name        string
	State       string
	SavePath    string
	ContentPath string
	FileNames   []string
}

// TorrentByHash reads one task by infohash. found=false means qBittorrent has
// no such task. Unlike the owner pipeline's observation it lists every file,
// because a data-deleting action must know everything qBittorrent would remove.
func (c Client) TorrentByHash(ctx context.Context, hash string) (TorrentFacts, bool, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return TorrentFacts{}, false, errors.New("qB torrent hash is required")
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return TorrentFacts{}, false, err
	}
	item, found, err := c.findTorrents(ctx, client, "hashes="+url.QueryEscape(hash))
	if err != nil {
		return TorrentFacts{}, false, fmt.Errorf("look up qB torrent: %w", err)
	}
	if !found {
		return TorrentFacts{}, false, nil
	}
	files, err := c.GetTorrentFiles(ctx, item.Hash)
	if err != nil {
		return TorrentFacts{}, false, err
	}
	facts := TorrentFacts{Hash: item.Hash, Name: item.Name, State: item.State, SavePath: item.SavePath, ContentPath: item.ContentPath}
	for _, file := range files {
		facts.FileNames = append(facts.FileNames, file.Name)
	}
	return facts, true, nil
}

// GetTorrentFiles returns the file list for a single torrent by hash.
func (c Client) GetTorrentFiles(ctx context.Context, hash string) ([]TorrentFile, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, errors.New("qB torrent hash is required")
	}
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/files?hash=" + url.QueryEscape(hash)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create qB files request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return nil, fmt.Errorf("request qB torrent files: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("qB torrent files failed with HTTP %d", response.StatusCode)
	}
	var files []TorrentFile
	if err := json.NewDecoder(response.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("decode qB torrent files: %w", err)
	}
	return files, nil
}

// ServerStatus is the qBittorrent server-state summary surfaced to the
// management dialog.
type ServerStatus struct {
	Version          string
	ConnectionStatus string
	DlSpeed          int64
	UpSpeed          int64
	FreeSpace        int64
	TotalSize        int64
}

// GetServerStatus reads the qBittorrent server state and application version in
// a single /sync/maindata call.
func (c Client) GetServerStatus(ctx context.Context) (ServerStatus, error) {
	client := c.client()
	if err := c.login(ctx, client); err != nil {
		return ServerStatus{}, err
	}
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/sync/maindata?rid=0"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ServerStatus{}, fmt.Errorf("create qB maindata request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return ServerStatus{}, fmt.Errorf("request qB server status: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ServerStatus{}, fmt.Errorf("qB server status failed with HTTP %d", response.StatusCode)
	}
	var payload struct {
		ServerState struct {
			ConnectionStatus string `json:"connection_status"`
			DlInfoSpeed      int64  `json:"dl_info_speed"`
			UpInfoSpeed      int64  `json:"up_info_speed"`
			FreeSpaceOnDisk  int64  `json:"free_space_on_disk"`
			TotalSize        int64  `json:"total_size"`
		} `json:"server_state"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return ServerStatus{}, fmt.Errorf("decode qB server status: %w", err)
	}
	status := ServerStatus{
		ConnectionStatus: payload.ServerState.ConnectionStatus,
		DlSpeed:          payload.ServerState.DlInfoSpeed,
		UpSpeed:          payload.ServerState.UpInfoSpeed,
		FreeSpace:        payload.ServerState.FreeSpaceOnDisk,
		TotalSize:        payload.ServerState.TotalSize,
	}
	// Fetch the application version separately -- /sync/maindata does not
	// include it.
	version, err := c.appVersion(ctx, client)
	if err != nil {
		return status, err
	}
	status.Version = version
	return status, nil
}

func (c Client) appVersion(ctx context.Context, client *http.Client) (string, error) {
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/app/version"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create qB version request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return "", fmt.Errorf("request qB version: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("qB version failed with HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 256))
	if err != nil {
		return "", fmt.Errorf("read qB version: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

func (c Client) login(ctx context.Context, client *http.Client) error {
	form := url.Values{"username": {c.Username}, "password": {c.Password}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoint, "/")+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create qB login request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := doRequest(client, request)
	if err != nil {
		return fmt.Errorf("login to qB: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("qB login refused with HTTP 403: %w", ErrAuthFailed)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("qB login failed with HTTP %d", response.StatusCode)
	}
	// qB answers 200 with "Fails." for a wrong username or password.
	body, _ := io.ReadAll(io.LimitReader(response.Body, 256))
	if strings.TrimSpace(string(body)) == "Fails." {
		return fmt.Errorf("qB login rejected the credentials: %w", ErrAuthFailed)
	}
	return nil
}

type TorrentInfo struct {
	AddedOn     int64  `json:"added_on"`
	ContentPath string `json:"content_path"`
	// Completed is the torrent's content-complete byte count. It is distinct from
	// Downloaded, which is the session's cumulative network traffic and includes
	// re-downloads and discarded pieces; only Completed may feed a progress
	// high-water mark. qB always reports it in torrents/info.
	Completed     int64   `json:"completed"`
	Hash          string  `json:"hash"`
	Name          string  `json:"name"`
	Progress      float64 `json:"progress"`
	SavePath      string  `json:"save_path"`
	State         string  `json:"state"`
	TrackerStatus string  `json:"tracker_status"`
	Size          int64   `json:"size"`
	Downloaded    int64   `json:"downloaded"`
	Uploaded      int64   `json:"uploaded"`
	Dlspeed       int64   `json:"dlspeed"`
	Upspeed       int64   `json:"upspeed"`
	Eta           int64   `json:"eta"`
	Category      string  `json:"category"`
	Tags          string  `json:"tags"`
	// RatioLimit and SeedingTimeLimit are the task's own share limits (-2 the
	// global limit, -1 no limit; minutes for the time).
	RatioLimit       float64 `json:"ratio_limit"`
	SeedingTimeLimit int64   `json:"seeding_time_limit"`
}

type TorrentFile struct {
	Name     string  `json:"name"`
	Progress float64 `json:"progress"`
	Size     int64   `json:"size"`
	Priority int     `json:"priority"`
}

func (c Client) findByTag(ctx context.Context, client *http.Client, tag string) (TorrentInfo, bool, error) {
	return c.findTorrents(ctx, client, "tag="+url.QueryEscape(tag))
}

func (c Client) findByProviderTaskID(ctx context.Context, client *http.Client, providerTaskID string) (TorrentInfo, bool, error) {
	query := "tag=" + url.QueryEscape(strings.TrimPrefix(providerTaskID, "tag:"))
	if strings.HasPrefix(providerTaskID, "hash:") {
		query = "hashes=" + url.QueryEscape(strings.TrimPrefix(providerTaskID, "hash:"))
	}
	return c.findTorrents(ctx, client, query)
}

func (c Client) findTorrents(ctx context.Context, client *http.Client, query string) (TorrentInfo, bool, error) {
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/info?" + query
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TorrentInfo{}, false, fmt.Errorf("create qB task lookup request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return TorrentInfo{}, false, fmt.Errorf("request qB task lookup: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return TorrentInfo{}, false, fmt.Errorf("qB task lookup failed with HTTP %d", response.StatusCode)
	}
	var torrents []TorrentInfo
	if err := json.NewDecoder(response.Body).Decode(&torrents); err != nil {
		return TorrentInfo{}, false, fmt.Errorf("decode qB task status: %w", err)
	}
	if len(torrents) == 0 {
		return TorrentInfo{}, false, nil
	}
	if strings.HasPrefix(query, "tag=") && len(torrents) > 1 {
		return TorrentInfo{}, false, fmt.Errorf("%w: stable operation tag", ErrLookupAmbiguous)
	}
	if strings.HasPrefix(query, "hashes=") {
		if len(torrents) > 1 {
			return TorrentInfo{}, false, fmt.Errorf("%w: frozen torrent hashes", ErrLookupAmbiguous)
		}
		return torrents[0], true, nil
	}
	// A single tag match is the only safe correlation. Never silently choose
	// one row from a duplicated stable tag because cancellation could leave the
	// other provider task running.
	sort.SliceStable(torrents, func(i, j int) bool {
		if torrents[i].AddedOn != torrents[j].AddedOn {
			return torrents[i].AddedOn > torrents[j].AddedOn
		}
		return torrents[i].Hash > torrents[j].Hash
	})
	return torrents[0], true, nil
}

// HashCandidates normalizes frozen info hashes into the qB task identities to
// query: lower-case, de-duplicated, and for a v2 hash also its first 40 hex
// digits. qBittorrent names a task by its v1 info hash when the torrent has one
// (v1 and hybrid torrents) and by the SHA-256 v2 info hash truncated to 40 hex
// digits otherwise (pure v2), so the v1 hash and the truncated v2 hash together
// cover every torrent version.
func HashCandidates(hashes ...string) []string {
	result := make([]string, 0, len(hashes)+1)
	seen := make(map[string]struct{}, len(hashes)+1)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	for _, value := range hashes {
		add(value)
		if trimmed := strings.TrimSpace(value); len(trimmed) == 64 {
			add(trimmed[:40])
		}
	}
	return result
}

// hashOccupied reports whether qB already holds a task for one of the hashes
// that does not carry the operation tag. A task carrying the tag is this
// submission's own task and never counts as occupation.
func (c Client) hashOccupied(ctx context.Context, client *http.Client, hashes []string, tag string) (bool, error) {
	if len(hashes) == 0 {
		return false, nil
	}
	endpoint := strings.TrimRight(c.Endpoint, "/") + "/api/v2/torrents/info?hashes=" + url.QueryEscape(strings.Join(hashes, "|"))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("create qB hash lookup request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return false, fmt.Errorf("request qB hash lookup: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("qB hash lookup failed with HTTP %d", response.StatusCode)
	}
	var torrents []TorrentInfo
	if err := json.NewDecoder(response.Body).Decode(&torrents); err != nil {
		return false, fmt.Errorf("decode qB hash lookup: %w", err)
	}
	for _, item := range torrents {
		if !hasTag(item.Tags, tag) {
			return true, nil
		}
	}
	return false, nil
}

// hasTag reads qB's comma-separated tag list ("a, b").
func hasTag(tags, tag string) bool {
	for _, value := range strings.Split(tags, ",") {
		if strings.TrimSpace(value) == tag {
			return true
		}
	}
	return false
}

func (c Client) artifactFiles(ctx context.Context, client *http.Client, item TorrentInfo) ([]domain.ProviderFile, error) {
	if item.Hash == "" {
		return nil, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/api/v2/torrents/files?hash="+url.QueryEscape(item.Hash), nil)
	if err != nil {
		return nil, fmt.Errorf("create qB torrent files request: %w", err)
	}
	response, err := doRequest(client, request)
	if err != nil {
		return nil, fmt.Errorf("request qB torrent files: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("qB torrent files failed with HTTP %d", response.StatusCode)
	}
	var files []TorrentFile
	if err := json.NewDecoder(response.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("decode qB torrent files: %w", err)
	}
	if len(files) == 0 {
		return nil, nil
	}
	sort.SliceStable(files, func(i, j int) bool {
		iRank, jRank := artifactPriority(files[i].Name), artifactPriority(files[j].Name)
		if iRank != jRank {
			return iRank < jRank
		}
		if files[i].Progress != files[j].Progress {
			return files[i].Progress > files[j].Progress
		}
		return files[i].Size > files[j].Size
	})
	paths := make([]domain.ProviderFile, 0, len(files))
	for _, file := range files {
		if file.Progress < 0.999 || strings.TrimSpace(file.Name) == "" {
			continue
		}
		path := file.Name
		if !filepath.IsAbs(path) {
			if hasParentTraversal(path) {
				continue
			}
			contentBase := filepath.Base(filepath.Clean(item.ContentPath))
			relative := filepath.Clean(path)
			switch {
			case contentBase == relative:
				// Single-file torrent: content_path already includes the name.
				path = item.ContentPath
			case strings.HasPrefix(relative, contentBase+string(filepath.Separator)):
				// Multi-file torrent: qB includes the torrent root in file.name,
				// while content_path points at that root directory.
				path = filepath.Join(filepath.Dir(item.ContentPath), relative)
			default:
				path = filepath.Join(item.ContentPath, relative)
			}
		}
		// The application trusts qB-reported paths directly under the same-path
		// deployment convention; relative traversal was already filtered above.
		path = filepath.Clean(path)
		paths = append(paths, domain.ProviderFile{Path: path, SizeBytes: file.Size, MediaKind: artifactMediaKind(path), Language: subtitleLanguage(path), Progress: file.Progress})
	}
	return paths, nil
}

func hasParentTraversal(value string) bool {
	value = strings.ReplaceAll(value, "\\", "/")
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func primaryArtifactPath(paths []string) string {
	for _, path := range paths {
		if artifactPriority(path) == 0 {
			return path
		}
	}
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			return path
		}
	}
	return ""
}

func artifactPriority(path string) int {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".webm", ".ts":
		return 0
	case ".ass", ".ssa", ".srt", ".sub", ".vtt":
		return 2
	default:
		return 1
	}
}

func artifactMediaKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".webm", ".ts":
		return "video"
	case ".ass", ".ssa", ".srt", ".sub", ".vtt":
		return "subtitle"
	default:
		return "other"
	}
}

func subtitleLanguage(path string) string {
	if artifactMediaKind(path) != "subtitle" {
		return ""
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	parts := strings.Split(base, ".")
	if len(parts) < 2 {
		return ""
	}
	candidate := strings.TrimSpace(parts[len(parts)-1])
	if candidate == "" || strings.ContainsAny(candidate, " /\\") {
		return ""
	}
	return candidate
}

// normalizeProviderState bounds the raw qB status string so it can be persisted
// alongside the mapped owner state. qB statuses are short identifiers; anything
// longer is a protocol surprise and is dropped rather than stored.
func normalizeProviderState(state string) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if len(state) > 64 {
		return ""
	}
	return state
}

// mapState reads qB's own verdict. A failure state wins over any progress, and
// a task is complete only once qB moves it to an upload state: progress is a
// rounded ratio, and a torrent stalled at 99.9% (a missing piece) still has
// full-size files that would pass the path+size inspection.
func mapState(state string) domain.TaskState {
	state = strings.ToLower(strings.TrimSpace(state))
	switch state {
	case "error", "missingfiles", "unknown":
		return domain.TaskFailed
	// qBittorrent 5 renamed pausedUP to stoppedUP.
	case "uploading", "stalledup", "pausedup", "stoppedup", "queuedup", "forcedup":
		return domain.TaskCompleted
	// checkingUP and moving keep a finished torrent's files in flux.
	case "pauseddl", "stoppeddl", "queued", "queueddl", "allocating", "checkingdl", "checkingup", "checkingresumedata", "moving", "metadl", "forcedmetadl", "forceddl", "stalleddl", "downloading":
		return domain.TaskDownloading
	default:
		return domain.TaskSubmitted
	}
}

func clampProgress(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (c Client) String() string {
	return "qBittorrent " + strings.TrimRight(c.Endpoint, "/") + " (timeout=" + strconv.FormatInt(c.timeout().Milliseconds(), 10) + "ms)"
}

// directTransport is the downloader transport. The downloader is a
// user-managed endpoint (same host, compose network or LAN), so it never goes
// through the container's HTTP_PROXY/HTTPS_PROXY egress proxy: a proxy cannot
// reach a compose service name or a private address, and the owner should not
// have to list the downloader in NO_PROXY for it to keep working.
var directTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}()

// DirectTransport returns the shared proxy-free downloader transport.
func DirectTransport() http.RoundTripper { return directTransport }

func (c Client) client() *http.Client {
	var client http.Client
	if c.HTTP != nil {
		client = *c.HTTP
	} else {
		client.Timeout = c.timeout()
		client.Transport = directTransport
	}
	if client.Jar == nil {
		client.Jar, _ = cookiejar.New(nil)
	}
	return &client
}

func (c Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 20 * time.Second
	}
	return c.Timeout
}
