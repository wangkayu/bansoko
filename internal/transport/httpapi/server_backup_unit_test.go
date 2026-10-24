package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accessdomain "bansoko/internal/accesscontrol/domain"
	backupapplication "bansoko/internal/backup/application"
	"bansoko/internal/transport/httpapi/generated"
)

type restoreRecorder struct{ called bool }

func (f *restoreRecorder) Info(context.Context) (backupapplication.Info, error) {
	return backupapplication.Info{}, nil
}

func (f *restoreRecorder) Export(context.Context) (*backupapplication.Export, error) {
	return &backupapplication.Export{Name: "bansoko-x.bansoko-backup"}, nil
}

func (f *restoreRecorder) Restore(_ context.Context, body io.Reader) (backupapplication.RestoreAccepted, error) {
	f.called = true
	_, _ = io.Copy(io.Discard, body)
	return backupapplication.RestoreAccepted{BackupCreatedAt: time.Unix(1760000000, 0).UTC()}, nil
}

func restoreAs(t *testing.T, account accessdomain.Account) (*httptest.ResponseRecorder, *restoreRecorder) {
	t.Helper()
	backup := &restoreRecorder{}
	server := &Server{deps: Dependencies{Backup: backup}}
	request := httptest.NewRequest(http.MethodPost, backupRestorePath, strings.NewReader("file"))
	request = request.WithContext(context.WithValue(request.Context(), accountKey, account))
	response := httptest.NewRecorder()
	server.RestoreBackup(response, request)
	return response, backup
}

func TestRestoreRefusesAPITokensWhateverTheirScopes(t *testing.T) {
	token := accessdomain.Account{ID: "a", Role: "owner", Scopes: append([]string(nil), accessdomain.AllScopes...)}
	response, backup := restoreAs(t, token)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "WEB_SESSION_REQUIRED") {
		t.Fatalf("token restore = %d %s, want 403 WEB_SESSION_REQUIRED", response.Code, response.Body.String())
	}
	if backup.called {
		t.Fatal("a token reached the restore service")
	}

	token.WebSession = true
	response, backup = restoreAs(t, token)
	if response.Code != http.StatusAccepted || !backup.called {
		t.Fatalf("web session restore = %d %s, want 202", response.Code, response.Body.String())
	}
}

func TestBackupDownloadTicketIsSingleUseAndExpires(t *testing.T) {
	server := &Server{deps: Dependencies{Backup: &restoreRecorder{}}}
	prepare := func() generated.BackupDownloadTicket {
		response := httptest.NewRecorder()
		server.PrepareBackupDownload(response, httptest.NewRequest(http.MethodPost, backupTicketPath, nil))
		if response.Code != http.StatusCreated {
			t.Fatalf("prepare = %d %s", response.Code, response.Body.String())
		}
		var ticket generated.BackupDownloadTicket
		if err := json.Unmarshal(response.Body.Bytes(), &ticket); err != nil {
			t.Fatal(err)
		}
		if len(ticket.Ticket) != 43 || ticket.FileName != "bansoko-x.bansoko-backup" {
			t.Fatalf("ticket = %+v", ticket)
		}
		return ticket
	}
	download := func(ticket string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		server.DownloadPreparedBackup(response, httptest.NewRequest(http.MethodGet, backupDownloadPath+"?ticket="+ticket, nil),
			generated.DownloadPreparedBackupParams{Ticket: ticket})
		return response
	}

	ticket := prepare()
	if first := download(ticket.Ticket); first.Code != http.StatusOK ||
		!strings.Contains(first.Header().Get("Content-Disposition"), "bansoko-x.bansoko-backup") {
		t.Fatalf("first download = %d %v", first.Code, first.Header())
	}
	if again := download(ticket.Ticket); again.Code != http.StatusForbidden || !strings.Contains(again.Body.String(), "BACKUP_TICKET_INVALID") {
		t.Fatalf("reused ticket = %d %s, want 403 BACKUP_TICKET_INVALID", again.Code, again.Body.String())
	}
	if unknown := download(strings.Repeat("A", 43)); unknown.Code != http.StatusForbidden {
		t.Fatalf("unknown ticket = %d, want 403", unknown.Code)
	}

	expired := prepare()
	if export := server.backupDownloads.take(expired.Ticket, time.Now().Add(backupTicketTTL)); export != nil {
		t.Fatal("an expired ticket still downloads")
	}

	// The store keeps a bounded number of prepared files; the oldest goes.
	oldest := prepare()
	for range maxPreparedBackups {
		prepare()
	}
	if response := download(oldest.Ticket); response.Code != http.StatusForbidden {
		t.Fatalf("evicted ticket = %d, want 403", response.Code)
	}
}

func TestBackupDownloadIsAnonymousOnlyThroughItsTicket(t *testing.T) {
	if !publicAPIPaths["GET "+backupDownloadPath] {
		t.Fatal("the ticket download must not require a bearer (a browser navigation cannot send one)")
	}
	for _, key := range []string{"GET " + backupExportPath, "POST " + backupTicketPath, "POST " + backupRestorePath} {
		if publicAPIPaths[key] {
			t.Fatalf("%s must require authentication", key)
		}
	}
}
