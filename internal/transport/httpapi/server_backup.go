package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"

	"bansoko/internal/apperrors"
	backupapplication "bansoko/internal/backup/application"
	"bansoko/internal/transport/httpapi/generated"
)

// BackupService is the database backup surface: export is a download and
// restore an upload; the app keeps no backup files.
type BackupService interface {
	Info(ctx context.Context) (backupapplication.Info, error)
	Export(ctx context.Context) (*backupapplication.Export, error)
	Restore(ctx context.Context, body io.Reader) (backupapplication.RestoreAccepted, error)
}

// backupTransferWindow replaces the server-wide read/write timeouts for one
// backup download or upload, which can take far longer than an API call.
const backupTransferWindow = 2 * time.Hour

// maxRestoreUploadBytes bounds the upload: the payload limit plus the header.
const maxRestoreUploadBytes = backupapplication.MaxPayloadBytes + 8<<10

// SetBackup wires the backup service. It is set once at composition, before
// the server starts serving.
func (s *Server) SetBackup(service BackupService) {
	if s != nil {
		s.deps.Backup = service
	}
}

// GetBackupInfo returns the running schema number and whether export and
// restore can run.
func (s *Server) GetBackupInfo(w http.ResponseWriter, r *http.Request) {
	if s.deps.Backup == nil {
		writeError(w, r, serviceNotConfigured("backup"))
		return
	}
	info, err := s.deps.Backup.Info(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	dto := generated.BackupInfo{
		SchemaVersion:    info.SchemaVersion,
		ExportAvailable:  info.ExportAvailable,
		RestoreAvailable: info.RestoreAvailable,
	}
	if info.RestoreUnavailableReason != "" {
		reason := generated.BackupRestoreUnavailableReason(info.RestoreUnavailableReason)
		dto.RestoreUnavailableReason = &reason
	}
	writeJSON(w, r.Context(), http.StatusOK, dto)
}

// ExportBackup dumps the database and streams one backup file in the same
// request. It serves API tokens holding the secrets scope; the WebUI prepares a
// download ticket instead so the browser can stream the file to disk.
func (s *Server) ExportBackup(w http.ResponseWriter, r *http.Request) {
	if s.deps.Backup == nil {
		writeError(w, r, serviceNotConfigured("backup"))
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(backupTransferWindow))
	export, err := s.deps.Backup.Export(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer export.Close()
	streamBackup(w, export)
}

func streamBackup(w http.ResponseWriter, export *backupapplication.Export) {
	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Content-Length", strconv.FormatInt(export.Size, 10))
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": export.Name}))
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// A broken connection after the headers cannot be reported; the browser
	// sees a short download against Content-Length, and a restore of such a
	// file fails its size check.
	_, _ = export.WriteTo(w)
}

// Browser downloads. The API authenticates with an Authorization header, which
// a browser navigation cannot send, and fetching the file into page memory
// does not scale. So the signed-in page asks the server to prepare the backup
// (dump and verify, errors answered as JSON), receives a random single-use
// ticket, and navigates to GET /backup/download?ticket=, which streams the
// prepared file to disk. The ticket is 256 random bits, kept only as its
// SHA-256 in this process, consumed when the download starts and expires after
// backupTicketTTL, when the prepared file is removed. Query strings are never
// logged.
const (
	backupTicketTTL         = 5 * time.Minute
	maxPreparedBackups      = 2
	backupTicketRandomBytes = 32
)

type preparedBackup struct {
	export  *backupapplication.Export
	expires time.Time
	timer   *time.Timer
	seq     uint64
}

type backupDownloadStore struct {
	mu      sync.Mutex
	pending map[[sha256.Size]byte]*preparedBackup
	seq     uint64
}

// add stores export under a new ticket; the oldest prepared file is dropped
// when the store is full.
func (d *backupDownloadStore) add(export *backupapplication.Export, now time.Time) (string, time.Time, error) {
	raw := make([]byte, backupTicketRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)
	key := sha256.Sum256([]byte(ticket))
	expires := now.Add(backupTicketTTL)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending == nil {
		d.pending = map[[sha256.Size]byte]*preparedBackup{}
	}
	for len(d.pending) >= maxPreparedBackups {
		var oldestKey [sha256.Size]byte
		var oldest *preparedBackup
		for k, p := range d.pending {
			if oldest == nil || p.seq < oldest.seq {
				oldestKey, oldest = k, p
			}
		}
		d.dropLocked(oldestKey)
	}
	d.seq++
	prepared := &preparedBackup{export: export, expires: expires, seq: d.seq}
	prepared.timer = time.AfterFunc(backupTicketTTL, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.pending[key] == prepared {
			d.dropLocked(key)
		}
	})
	d.pending[key] = prepared
	return ticket, expires, nil
}

// take consumes a ticket: the caller owns and closes the returned export.
func (d *backupDownloadStore) take(ticket string, now time.Time) *backupapplication.Export {
	key := sha256.Sum256([]byte(ticket))
	d.mu.Lock()
	defer d.mu.Unlock()
	prepared, ok := d.pending[key]
	if !ok {
		return nil
	}
	delete(d.pending, key)
	prepared.timer.Stop()
	if !now.Before(prepared.expires) {
		_ = prepared.export.Close()
		return nil
	}
	return prepared.export
}

func (d *backupDownloadStore) dropLocked(key [sha256.Size]byte) {
	if prepared, ok := d.pending[key]; ok {
		delete(d.pending, key)
		prepared.timer.Stop()
		_ = prepared.export.Close()
	}
}

// PrepareBackupDownload dumps and verifies a backup and returns the ticket
// that downloads it.
func (s *Server) PrepareBackupDownload(w http.ResponseWriter, r *http.Request) {
	if s.deps.Backup == nil {
		writeError(w, r, serviceNotConfigured("backup"))
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(backupTransferWindow))
	export, err := s.deps.Backup.Export(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	ticket, expires, err := s.backupDownloads.add(export, time.Now())
	if err != nil {
		_ = export.Close()
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, generated.BackupDownloadTicket{
		Ticket:    ticket,
		FileName:  export.Name,
		SizeBytes: export.Size,
		ExpiresAt: expires.UTC(),
	})
}

// DownloadPreparedBackup streams the file a ticket names. The ticket is the
// credential; the route is otherwise anonymous.
func (s *Server) DownloadPreparedBackup(w http.ResponseWriter, r *http.Request, params generated.DownloadPreparedBackupParams) {
	export := s.backupDownloads.take(params.Ticket, time.Now())
	if export == nil {
		writeError(w, r, &APIError{Status: http.StatusForbidden, Code: "BACKUP_TICKET_INVALID", Message: "The download link is unknown, used or expired. Export again."})
		return
	}
	defer export.Close()
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(backupTransferWindow))
	streamBackup(w, export)
}

// RestoreBackup replaces all data with an uploaded backup. Every refusal
// leaves the current data unchanged; on 202 the service restarts on it.
func (s *Server) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	if s.deps.Backup == nil {
		writeError(w, r, serviceNotConfigured("backup"))
		return
	}
	// Replacing the whole database is for the owner at the keyboard only: an
	// API token (and so MCP or any agent) is refused whatever its scopes.
	if account, ok := AccountFrom(r.Context()); !ok || !account.WebSession {
		writeError(w, r, webSessionRequired())
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(backupTransferWindow))
	_ = controller.SetWriteDeadline(time.Now().Add(backupTransferWindow))
	body := http.MaxBytesReader(w, r.Body, maxRestoreUploadBytes)
	accepted, err := s.deps.Backup.Restore(r.Context(), body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "BACKUP_FILE_TOO_LARGE", Message: "The backup file is larger than a restore accepts."}
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusAccepted, generated.BackupRestoreAccepted{
		Restarting:      true,
		BackupCreatedAt: accepted.BackupCreatedAt,
	})
}
