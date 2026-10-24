package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/configuration/domain"
	"bansoko/internal/download/qbittorrent"
)

// QBManagementService is the qBittorrent operations management boundary. It
// proxies the qB WebUI API for the built-in management dialog and never
// mutates the DownloadTask owner truth. Every call resolves credentials by
// connection_id so the dialog can target any configured qBittorrent instance.
type QBManagementService struct {
	owner   *Service
	http    *http.Client
	timeout time.Duration
}

// NewQBManagementService constructs a management service backed by the same
// configuration owner that resolves provider credentials. The optional http
// client overrides the default transport (e.g. for tests); production leaves
// it nil so each call uses a fresh cookie-jar client.
func NewQBManagementService(owner *Service, http *http.Client) QBManagementService {
	return QBManagementService{owner: owner, http: http, timeout: 20 * time.Second}
}

func (s QBManagementService) client(ctx context.Context, connectionID string) (qbittorrent.Client, error) {
	if s.owner == nil {
		return qbittorrent.Client{}, errors.New("configuration owner is not configured")
	}
	connection, err := s.owner.GetConnection(ctx, connectionID)
	if err != nil {
		return qbittorrent.Client{}, err
	}
	if connection.Kind != domain.ConnectionQBittorrent {
		return qbittorrent.Client{}, &apperrors.BadRequest{Code: "CONNECTION_KIND_MISMATCH", Message: "connection is not a qBittorrent instance"}
	}
	if connection.QBittorrentConfig == nil {
		return qbittorrent.Client{}, &apperrors.BadRequest{Code: "QB_CONNECTION_NOT_CONFIGURED", Message: "qBittorrent connection has no typed config"}
	}
	resolver := s.owner.secretResolver()
	if resolver == nil || strings.TrimSpace(connection.SecretRef) == "" {
		return qbittorrent.Client{}, &apperrors.BadRequest{Code: "QB_CONNECTION_NOT_CONFIGURED", Message: "qBittorrent credentials are unavailable"}
	}
	password, found := resolver.Resolve(connection.SecretRef)
	if !found || strings.TrimSpace(password) == "" {
		return qbittorrent.Client{}, &apperrors.BadRequest{Code: "QB_CONNECTION_NOT_CONFIGURED", Message: "qBittorrent credentials are unavailable"}
	}
	return qbittorrent.Client{
		Endpoint: connection.Endpoint,
		Username: connection.QBittorrentConfig.Username,
		Password: password,
		HTTP:     s.http,
		Timeout:  s.timeout,
	}, nil
}

func (s QBManagementService) ListTorrents(ctx context.Context, connectionID string) ([]domain.QBittorrentTorrentView, error) {
	client, err := s.client(ctx, connectionID)
	if err != nil {
		// An unknown connection is a typed 404, not an internal 500. The
		// contract declares 404 for this endpoint; a missing connection row
		// must surface as NOT_FOUND without leaking internals. Other methods
		// in this family keep their existing connection-resolution semantics;
		// a missing torrent hash is verified by ensureTorrentExists instead.
		if errors.Is(err, ErrConnectionNotFound) {
			return nil, &apperrors.NotFound{Resource: "connection", ID: connectionID}
		}
		return nil, err
	}
	items, err := client.ListTorrents(ctx)
	if err != nil {
		return nil, mapQBError(err)
	}
	records, err := s.torrentRecords(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	result := make([]domain.QBittorrentTorrentView, 0, len(items))
	for _, item := range items {
		view := mapTorrent(item)
		view.AcquisitionOwner = records.inFlight.of(item)
		view.Origin = records.origin(item)
		result = append(result, view)
	}
	return result, nil
}

// torrentOwners indexes acquisitions by the two identities a qB task can
// carry: its infohash (recorded once the download is observed, or a `hash:`
// provider task id) and the stable correlation tag the submit attaches
// (`tag:` provider task id / stable_tag). A download in progress is usually
// known only by its tag, so both must be consulted.
type torrentOwners struct {
	byHash map[string]string
	byTag  map[string]string
}

func newTorrentOwners() torrentOwners {
	return torrentOwners{byHash: map[string]string{}, byTag: map[string]string{}}
}

func addTorrentOwner(index map[string]string, key, kind string) {
	if key == "" {
		return
	}
	// A one-shot never loses its tag to a subscription sharing the identity.
	if index[key] != "one_shot" {
		index[key] = kind
	}
}

// add indexes one download record's identities under its owner kind.
func (o torrentOwners) add(infoHash, externalID, stableTag, kind string) {
	addTorrentOwner(o.byHash, strings.ToLower(strings.TrimSpace(infoHash)), kind)
	switch {
	case strings.HasPrefix(externalID, "hash:"):
		addTorrentOwner(o.byHash, strings.ToLower(strings.TrimSpace(strings.TrimPrefix(externalID, "hash:"))), kind)
	case strings.HasPrefix(externalID, "tag:"):
		addTorrentOwner(o.byTag, strings.TrimSpace(strings.TrimPrefix(externalID, "tag:")), kind)
	}
	addTorrentOwner(o.byTag, strings.TrimSpace(stableTag), kind)
}

// of returns the owner kind of one listed qB task, or "" when no running
// acquisition owns it.
func (o torrentOwners) of(item qbittorrent.TorrentInfo) string {
	owner := o.byHash[strings.ToLower(item.Hash)]
	for _, tag := range strings.Split(item.Tags, ",") {
		if kind := o.byTag[strings.TrimSpace(tag)]; kind != "" && owner != "one_shot" {
			owner = kind
		}
	}
	return owner
}

// torrentOriginIndex keeps, per identity, every owner kind a download record
// on this connection claimed, so a conflict stays visible instead of being
// resolved by precedence.
type torrentOriginIndex struct {
	byHash map[string]map[string]bool
	byTag  map[string]map[string]bool
}

func addOriginKind(index map[string]map[string]bool, key, kind string) {
	if key == "" {
		return
	}
	if index[key] == nil {
		index[key] = map[string]bool{}
	}
	index[key][kind] = true
}

func (o torrentOriginIndex) add(infoHash, externalID, stableTag, kind string) {
	addOriginKind(o.byHash, strings.ToLower(strings.TrimSpace(infoHash)), kind)
	switch {
	case strings.HasPrefix(externalID, "hash:"):
		addOriginKind(o.byHash, strings.ToLower(strings.TrimSpace(strings.TrimPrefix(externalID, "hash:"))), kind)
	case strings.HasPrefix(externalID, "tag:"):
		addOriginKind(o.byTag, strings.TrimSpace(strings.TrimPrefix(externalID, "tag:")), kind)
	}
	addOriginKind(o.byTag, strings.TrimSpace(stableTag), kind)
}

// torrentRecordIndex is what the download records on one connection say about
// listed qB tasks: who owns a task now (running acquisitions only) and who
// created it (every record, running or finished).
type torrentRecordIndex struct {
	inFlight torrentOwners
	origins  torrentOriginIndex
}

func newTorrentRecordIndex() torrentRecordIndex {
	return torrentRecordIndex{
		inFlight: newTorrentOwners(),
		origins:  torrentOriginIndex{byHash: map[string]map[string]bool{}, byTag: map[string]map[string]bool{}},
	}
}

// qbSystemTagPrefix is the correlation tag prefix every bansoko submit
// attaches; user tags may not use it (QBittorrentConnectionConfig validation).
const qbSystemTagPrefix = "bansoko-"

// origin decides who created one listed qB task:
//   - the correlation tag is written by the submit that created the task, so a
//     tag match decides first; the infohash (which an adopting download can
//     share) decides only when no tag matches;
//   - the matching records must agree on one kind, else the origin is unknown;
//   - with no matching record, a reserved bansoko- tag still proves bansoko
//     created the task but not for whom (its record was reclaimed after
//     one-shot retention or deleted with its subscription): unknown;
//   - otherwise the task was added outside bansoko: external.
func (r torrentRecordIndex) origin(item qbittorrent.TorrentInfo) string {
	tagKinds := map[string]bool{}
	systemTag := false
	for _, raw := range strings.Split(item.Tags, ",") {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(tag), qbSystemTagPrefix) {
			systemTag = true
		}
		for kind := range r.origins.byTag[tag] {
			tagKinds[kind] = true
		}
	}
	kinds := tagKinds
	if len(kinds) == 0 {
		kinds = r.origins.byHash[strings.ToLower(strings.TrimSpace(item.Hash))]
	}
	switch {
	case len(kinds) == 1:
		for kind := range kinds {
			return kind
		}
	case len(kinds) > 1:
		return "unknown"
	case systemTag:
		return "unknown"
	}
	return "external"
}

// acquisitionOwnerKind maps an acquisition entry kind to the product owner
// kind: a one-shot is one_shot, the automatic chain and the add-subscription
// download are subscription work.
func acquisitionOwnerKind(entryKind string) (string, bool) {
	switch entryKind {
	case "one_shot":
		return "one_shot", true
	case "automatic", "subscription_add":
		return "subscription", true
	}
	return "", false
}

// torrentRecords reads every download record on this connection (running or
// finished) with its acquisition's entry kind, by infohash and correlation
// tag. The management dialog is a qB-only surface, so ownership is read here
// from the acquisition owner without ever mutating it. A record whose entry
// kind has no owner kind fails the read rather than guessing one.
func (s QBManagementService) torrentRecords(ctx context.Context, connectionID string) (torrentRecordIndex, error) {
	index := newTorrentRecordIndex()
	if s.owner == nil || s.owner.db == nil {
		return index, nil
	}
	rows, err := s.owner.db.QueryContext(ctx, `
SELECT COALESCE(t.provider_correlation->>'info_hash', ''),
       COALESCE(t.provider_correlation->>'external_id', ''),
       COALESCE(t.provider_correlation->>'stable_tag', ''),
       a.entry_kind, a.state = 'running'
FROM download_tasks t JOIN acquisitions a ON a.id = t.acquisition_id
WHERE t.qbittorrent_connection_id = $1 AND t.provider_correlation IS NOT NULL`, connectionID)
	if err != nil {
		return torrentRecordIndex{}, fmt.Errorf("read torrent owners: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var infoHash, externalID, stableTag, entryKind string
		var running bool
		if err := rows.Scan(&infoHash, &externalID, &stableTag, &entryKind, &running); err != nil {
			return torrentRecordIndex{}, fmt.Errorf("scan torrent owner: %w", err)
		}
		kind, ok := acquisitionOwnerKind(entryKind)
		if !ok {
			return torrentRecordIndex{}, fmt.Errorf("read torrent owners: acquisition entry kind %q has no owner kind", entryKind)
		}
		index.origins.add(infoHash, externalID, stableTag, kind)
		if running {
			index.inFlight.add(infoHash, externalID, stableTag, kind)
		}
	}
	if err := rows.Err(); err != nil {
		return torrentRecordIndex{}, fmt.Errorf("read torrent owners: %w", err)
	}
	return index, nil
}

// ensureTorrentExists probes the qBittorrent instance for the torrent hash
// before an action. The upstream pause/resume endpoints answer HTTP 200 even
// for unknown hashes (a silent no-op), so the management boundary verifies
// existence itself and translates a missing hash into a typed NotFound. A
// probe failure keeps the stable QB_* semantics; the natural TOCTOU window
// between probe and action is acceptable for a single-user deployment dialog.
func ensureTorrentExists(ctx context.Context, client qbittorrent.Client, hash string) error {
	items, err := client.ListTorrents(ctx)
	if err != nil {
		return mapQBError(err)
	}
	for _, item := range items {
		if strings.EqualFold(item.Hash, hash) {
			return nil
		}
	}
	return &apperrors.NotFound{Resource: "torrent", ID: hash}
}

func (s QBManagementService) PauseTorrent(ctx context.Context, connectionID, hash string) error {
	client, err := s.client(ctx, connectionID)
	if err != nil {
		return err
	}
	if err := ensureTorrentExists(ctx, client, hash); err != nil {
		return err
	}
	if err := client.PauseTorrent(ctx, hash); err != nil {
		return mapQBError(err)
	}
	return nil
}

func (s QBManagementService) ResumeTorrent(ctx context.Context, connectionID, hash string) error {
	client, err := s.client(ctx, connectionID)
	if err != nil {
		return err
	}
	if err := ensureTorrentExists(ctx, client, hash); err != nil {
		return err
	}
	if err := client.ResumeTorrent(ctx, hash); err != nil {
		return mapQBError(err)
	}
	return nil
}

// DeleteTorrent removes a torrent from qB. While the torrent belongs to a
// running acquisition (subscription or one-shot) every delete through this
// manager is refused with a typed 409 before any qB call: post-processing still
// reads the torrent and its files from qB (task lookup, file list, placement).
// Once the acquisition is terminal (succeeded, failed or cancelled) the torrent
// is unowned and both forms are allowed. A delete made in qBittorrent itself
// bypasses this guard; the qB sync detects it (two consecutive definitive
// absences) and the record ends cancelled, deleted in the downloader.
func (s QBManagementService) DeleteTorrent(ctx context.Context, connectionID, hash string, deleteFiles bool) error {
	client, err := s.client(ctx, connectionID)
	if err != nil {
		return err
	}
	records, err := s.torrentRecords(ctx, connectionID)
	if err != nil {
		return err
	}
	// The task's tags are part of its identity: a download in progress is
	// usually owned through its correlation tag only, so read them from qB.
	// An unreadable list fails closed before any delete is sent.
	items, err := client.ListTorrents(ctx)
	if err != nil {
		return mapQBError(err)
	}
	target := qbittorrent.TorrentInfo{Hash: hash}
	for _, item := range items {
		if strings.EqualFold(item.Hash, hash) {
			target = item
			break
		}
	}
	if records.inFlight.of(target) != "" {
		return &apperrors.Business{Status: http.StatusConflict, Code: "QB_TORRENT_IN_USE", Message: "the torrent belongs to a download that is still being processed"}
	}
	if err := client.DeleteTorrent(ctx, hash, deleteFiles); err != nil {
		return mapQBError(err)
	}
	return nil
}

func (s QBManagementService) GetTorrentFiles(ctx context.Context, connectionID, hash string) ([]domain.QBittorrentTorrentFileView, error) {
	client, err := s.client(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	files, err := client.GetTorrentFiles(ctx, hash)
	if err != nil {
		return nil, mapQBError(err)
	}
	result := make([]domain.QBittorrentTorrentFileView, 0, len(files))
	for _, file := range files {
		result = append(result, mapTorrentFile(file))
	}
	return result, nil
}

func (s QBManagementService) GetServerStatus(ctx context.Context, connectionID string) (domain.QBittorrentServerStatusView, error) {
	client, err := s.client(ctx, connectionID)
	if err != nil {
		return domain.QBittorrentServerStatusView{}, err
	}
	status, err := client.GetServerStatus(ctx)
	if err != nil {
		return domain.QBittorrentServerStatusView{}, mapQBError(err)
	}
	return domain.QBittorrentServerStatusView{
		Version:          status.Version,
		ConnectionStatus: status.ConnectionStatus,
		DownloadSpeed:    status.DlSpeed,
		UploadSpeed:      status.UpSpeed,
		FreeSpace:        status.FreeSpace,
		TotalSize:        status.TotalSize,
	}, nil
}

func mapTorrent(item qbittorrent.TorrentInfo) domain.QBittorrentTorrentView {
	return domain.QBittorrentTorrentView{
		Hash:          item.Hash,
		Name:          item.Name,
		State:         item.State,
		Progress:      item.Progress,
		Size:          item.Size,
		Downloaded:    item.Downloaded,
		Uploaded:      item.Uploaded,
		DownloadSpeed: item.Dlspeed,
		UploadSpeed:   item.Upspeed,
		Eta:           item.Eta,
		Category:      item.Category,
		Tags:          item.Tags,
		SavePath:      item.SavePath,
		AddedOn:       item.AddedOn,
	}
}

func mapTorrentFile(file qbittorrent.TorrentFile) domain.QBittorrentTorrentFileView {
	return domain.QBittorrentTorrentFileView{
		Name:     file.Name,
		Size:     file.Size,
		Progress: file.Progress,
		Priority: file.Priority,
	}
}

// mapQBError translates a low-level qB client error into a typed application
// error so the transport layer can surface a meaningful code to the dialog.
func mapQBError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	// Only a login the instance answered and refused is a credential problem;
	// "login to qB: …" wraps a transport error (connection refused, timeout),
	// which is the instance being unreachable.
	case strings.Contains(message, "qB login failed"):
		return &apperrors.BadRequest{Code: "QB_AUTH_FAILED", Message: "qBittorrent authentication failed; check the configured credentials"}
	case strings.Contains(message, "login to qB") || strings.Contains(message, "request qB") || strings.Contains(message, "check qB") || strings.Contains(message, "dial"):
		return &apperrors.BadRequest{Code: "QB_UNREACHABLE", Message: fmt.Sprintf("qBittorrent instance is unreachable: %s", message)}
	default:
		return &apperrors.BadRequest{Code: "QB_REQUEST_FAILED", Message: message}
	}
}
