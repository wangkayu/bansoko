package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// qbManagementFake is a controllable QBManagementService for the passthrough
// handlers. Each method routes to a single error/result hook.
type qbManagementFake struct {
	listTorrents    func(ctx context.Context, connectionID string) ([]configurationdomain.QBittorrentTorrentView, error)
	pauseTorrent    func(ctx context.Context, connectionID, hash string) error
	resumeTorrent   func(ctx context.Context, connectionID, hash string) error
	deleteTorrent   func(ctx context.Context, connectionID, hash string, deleteFiles bool) error
	getTorrentFiles func(ctx context.Context, connectionID, hash string) ([]configurationdomain.QBittorrentTorrentFileView, error)
	getServerStatus func(ctx context.Context, connectionID string) (configurationdomain.QBittorrentServerStatusView, error)
}

func (f *qbManagementFake) ListTorrents(ctx context.Context, connectionID string) ([]configurationdomain.QBittorrentTorrentView, error) {
	return f.listTorrents(ctx, connectionID)
}

func (f *qbManagementFake) PauseTorrent(ctx context.Context, connectionID, hash string) error {
	return f.pauseTorrent(ctx, connectionID, hash)
}

func (f *qbManagementFake) ResumeTorrent(ctx context.Context, connectionID, hash string) error {
	return f.resumeTorrent(ctx, connectionID, hash)
}

func (f *qbManagementFake) DeleteTorrent(ctx context.Context, connectionID, hash string, deleteFiles bool) error {
	return f.deleteTorrent(ctx, connectionID, hash, deleteFiles)
}

func (f *qbManagementFake) GetTorrentFiles(ctx context.Context, connectionID, hash string) ([]configurationdomain.QBittorrentTorrentFileView, error) {
	return f.getTorrentFiles(ctx, connectionID, hash)
}

func (f *qbManagementFake) GetServerStatus(ctx context.Context, connectionID string) (configurationdomain.QBittorrentServerStatusView, error) {
	return f.getServerStatus(ctx, connectionID)
}

// Issue #7 regression: the qB torrents list endpoint maps an unknown
// connection (typed application NotFound from the configuration owner) into a
// structured 404 ErrorResponse — never an INTERNAL 500 and no stack or driver
// internals leak. A live list passes through unchanged and upstream failure
// codes keep their original status mapping.
func TestListQBittorrentTorrentsUnknownConnectionIsStructuredNotFound(t *testing.T) {
	server := &Server{deps: Dependencies{QBManagement: &qbManagementFake{
		listTorrents: func(ctx context.Context, connectionID string) ([]configurationdomain.QBittorrentTorrentView, error) {
			return nil, &apperrors.NotFound{Resource: "connection", ID: connectionID}
		},
	}}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/config/connections/nope/qbittorrent/torrents", nil)
	server.ListQBittorrentTorrents(recorder, request, "nope")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown connection status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("structured error expected: %v", err)
	}
	if response.Code != "NOT_FOUND" {
		t.Fatalf("error code: %s", response.Code)
	}
	body := recorder.Body.String()
	if !json.Valid(recorder.Body.Bytes()) {
		t.Fatalf("body must be valid JSON: %q", body)
	}
	for _, leaked := range []string{"sql:", "Stack:", "connection \\%q not found"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("internals leaked in body %q", body)
		}
	}
}

func TestListQBittorrentTorrentsHappyPathPassthrough(t *testing.T) {
	server := &Server{deps: Dependencies{QBManagement: &qbManagementFake{
		listTorrents: func(ctx context.Context, connectionID string) ([]configurationdomain.QBittorrentTorrentView, error) {
			hash := "abc123"
			return []configurationdomain.QBittorrentTorrentView{{Hash: hash, Name: "Show EP01", State: "downloading"}}, nil
		},
	}}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/config/connections/qb1/qbittorrent/torrents", nil)
	server.ListQBittorrentTorrents(recorder, request, "qb1")

	if recorder.Code != http.StatusOK {
		t.Fatalf("happy path status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var torrents []generated.QBittorrentTorrent
	if err := json.Unmarshal(recorder.Body.Bytes(), &torrents); err != nil {
		t.Fatal(err)
	}
	if len(torrents) != 1 || torrents[0].Hash != "abc123" {
		t.Fatalf("torrent list mismatch: %+v", torrents)
	}
}

func TestListQBittorrentTorrentsUnreachableKeepsStatusMapping(t *testing.T) {
	server := &Server{deps: Dependencies{QBManagement: &qbManagementFake{
		listTorrents: func(ctx context.Context, connectionID string) ([]configurationdomain.QBittorrentTorrentView, error) {
			return nil, &apperrors.BadRequest{Code: "QB_UNREACHABLE", Message: "qBittorrent instance is unreachable"}
		},
	}}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/config/connections/qb1/qbittorrent/torrents", nil)
	server.ListQBittorrentTorrents(recorder, request, "qb1")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unreachable status must keep its existing mapping: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "QB_UNREACHABLE" {
		t.Fatalf("upstream error code changed: %s", response.Code)
	}
}

// A delete refused because a running acquisition owns the torrent maps to a
// structured 409 with the stable code, with or without delete_files.
func TestDeleteQBittorrentTorrentOwnedByRunningAcquisitionIs409(t *testing.T) {
	server := &Server{deps: Dependencies{QBManagement: &qbManagementFake{
		deleteTorrent: func(ctx context.Context, connectionID, hash string, deleteFiles bool) error {
			return &apperrors.Business{Status: http.StatusConflict, Code: "QB_TORRENT_IN_USE", Message: "still being processed"}
		},
	}}}
	for _, deleteFiles := range []bool{true, false} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodDelete, "/api/v1/config/connections/c/qbittorrent/torrents/abc", nil)
		server.DeleteQBittorrentTorrent(recorder, request, "c", "abc", generated.DeleteQBittorrentTorrentParams{DeleteFiles: &deleteFiles})
		var response generated.ErrorResponse
		if recorder.Code != http.StatusConflict || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response.Code != "QB_TORRENT_IN_USE" {
			t.Fatalf("files=%v: status %d body=%s", deleteFiles, recorder.Code, recorder.Body.String())
		}
	}
}
