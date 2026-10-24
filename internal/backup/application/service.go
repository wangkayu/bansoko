package application

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/migrations"
)

// MaxPayloadBytes bounds an uploaded backup. It is far above any database this
// app produces and only stops an unbounded upload from filling the disk.
const MaxPayloadBytes int64 = 8 << 30

// Restore unavailability reasons, mirrored by the OpenAPI enum.
const (
	ReasonToolsUnavailable       = "tools_unavailable"
	ReasonDatabaseUnsupported    = "database_unsupported"
	ReasonPrivilegesInsufficient = "privileges_insufficient"
	ReasonRestoreInProgress      = "restore_in_progress"
)

// Tools is the whitelisted PostgreSQL client tooling, connected as the schema
// owner (the runtime role cannot read every table).
type Tools interface {
	// Dump writes a custom-format archive of the deployment database.
	Dump(ctx context.Context, outPath string) error
	// Verify proves an archive parses (pg_restore --list) without applying it.
	Verify(ctx context.Context, path string) error
	// RestoreInto loads an archive into an empty database in one transaction.
	RestoreInto(ctx context.Context, path, database string) error
}

// Cluster opens schema-owner connections to databases on the deployment's
// PostgreSQL server.
type Cluster interface {
	// Database is the deployment database name.
	Database() string
	// Open connects to database as the schema owner.
	Open(ctx context.Context, database string) (*sql.DB, error)
}

// Options wires the service. Tools or Cluster may be nil; the surface then
// reports the capability as unavailable instead of refusing startup.
type Options struct {
	// DB is the serving pool; it reads the running schema version.
	DB        *sql.DB
	Tools     Tools
	Cluster   Cluster
	SchemaDir string
	// TempDir holds the per-operation temporary files (default os.TempDir()).
	TempDir string
	Logger  observabilityport.RuntimeLogger
	Now     func() time.Time
}

// Info is the read model of the backup panel.
type Info struct {
	SchemaVersion            string
	ExportAvailable          bool
	RestoreAvailable         bool
	RestoreUnavailableReason string
}

// RestoreAccepted answers an accepted restore: the verified data is ready and
// the service restarts to switch to it.
type RestoreAccepted struct {
	BackupCreatedAt time.Time
}

// Service exports and restores the deployment database.
type Service struct {
	db        *sql.DB
	tools     Tools
	cluster   Cluster
	schemaDir string
	tempDir   string
	logger    observabilityport.RuntimeLogger
	now       func() time.Time

	mu        sync.Mutex
	restoring bool
	// ready is the verified restored database waiting for the handoff.
	ready   string
	handoff chan struct{}
}

// NewService validates the wiring.
func NewService(opts Options) (*Service, error) {
	if opts.DB == nil {
		return nil, errors.New("backup service requires a database")
	}
	if opts.Logger == nil {
		opts.Logger = observabilityport.NopRuntimeLogger{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.TempDir == "" {
		opts.TempDir = os.TempDir()
	}
	// A process that stopped mid-export (a prepared browser download included)
	// or mid-restore leaves its workspace behind; the container keeps /tmp
	// across restarts, so this process removes them before it makes new ones.
	for _, pattern := range []string{"bansoko-export-*", "bansoko-restore-*"} {
		stale, _ := filepath.Glob(filepath.Join(opts.TempDir, pattern))
		for _, dir := range stale {
			_ = os.RemoveAll(dir)
		}
	}
	return &Service{
		db:        opts.DB,
		tools:     opts.Tools,
		cluster:   opts.Cluster,
		schemaDir: opts.SchemaDir,
		tempDir:   opts.TempDir,
		logger:    opts.Logger,
		now:       opts.Now,
		handoff:   make(chan struct{}),
	}, nil
}

// HandoffRequested is closed once a restore is verified and ready: the
// composition root then stops serving and the background runtime, closes the
// serving pool, calls Activate (or Abandon) and exits so the process restarts
// on the restored data.
func (s *Service) HandoffRequested() <-chan struct{} { return s.handoff }

// Info reports the running schema and whether export and restore can run.
func (s *Service) Info(ctx context.Context) (Info, error) {
	version, err := s.runningSchema(ctx)
	if err != nil {
		return Info{}, err
	}
	number, err := SchemaNumber(version)
	if err != nil {
		return Info{}, err
	}
	info := Info{SchemaVersion: number, ExportAvailable: s.tools != nil}
	if reason := s.restoreBlocker(ctx); reason != "" {
		info.RestoreUnavailableReason = reason
	} else {
		info.RestoreAvailable = true
	}
	if s.busy() {
		info.ExportAvailable = false
	}
	return info, nil
}

func (s *Service) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restoring || s.ready != ""
}

// restoreBlocker returns why a restore cannot run, or "".
func (s *Service) restoreBlocker(ctx context.Context) string {
	if s.busy() {
		return ReasonRestoreInProgress
	}
	if s.tools == nil {
		return ReasonToolsUnavailable
	}
	if s.cluster == nil || !validDatabaseName(s.cluster.Database()) {
		return ReasonDatabaseUnsupported
	}
	admin, err := s.cluster.Open(ctx, maintenanceDatabase)
	if err != nil {
		return ReasonDatabaseUnsupported
	}
	defer admin.Close()
	ok, err := canSwapDatabase(ctx, admin, s.cluster.Database())
	if err != nil || !ok {
		return ReasonPrivilegesInsufficient
	}
	return ""
}

func (s *Service) runningSchema(ctx context.Context) (string, error) {
	return latestSchemaVersion(ctx, s.db)
}

func latestSchemaVersion(ctx context.Context, db *sql.DB) (string, error) {
	var version string
	if err := db.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version); err != nil {
		return "", fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// Export is one prepared backup file: its header and the verified payload in
// a temporary directory that Close removes.
type Export struct {
	Name    string
	Header  Header
	Size    int64
	encoded []byte
	payload *os.File
	dir     string
}

// WriteTo streams the header and the payload.
func (e *Export) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(e.encoded)
	written := int64(n)
	if err != nil {
		return written, err
	}
	if _, err := e.payload.Seek(0, io.SeekStart); err != nil {
		return written, err
	}
	copied, err := io.Copy(w, e.payload)
	return written + copied, err
}

// Close removes the temporary files.
func (e *Export) Close() error {
	if e.payload != nil {
		_ = e.payload.Close()
	}
	return os.RemoveAll(e.dir)
}

// Export dumps the database to a temporary file, verifies it, and returns it
// with its header. The caller streams it and closes it.
func (s *Service) Export(ctx context.Context) (*Export, error) {
	if s.busy() {
		return nil, restoreInProgress()
	}
	if s.tools == nil {
		return nil, &apperrors.Business{Status: 409, Code: "BACKUP_UNAVAILABLE", Message: "The database backup tools are not available on this deployment."}
	}
	version, err := s.runningSchema(ctx)
	if err != nil {
		return nil, err
	}
	createdAt := s.now().UTC().Truncate(time.Second)
	dir, err := os.MkdirTemp(s.tempDir, "bansoko-export-*")
	if err != nil {
		return nil, exportFailed()
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "payload.dump")
	if err := s.tools.Dump(ctx, path); err != nil {
		s.warn(ctx, "export", "backup_export_dump_failed", "dump")
		return nil, exportFailed()
	}
	if err := s.tools.Verify(ctx, path); err != nil {
		s.warn(ctx, "export", "backup_export_verify_failed", "verify")
		return nil, exportFailed()
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, exportFailed()
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil || size <= 0 {
		_ = file.Close()
		return nil, exportFailed()
	}
	header := Header{
		App: AppName, Format: FormatVersion, SchemaVersion: version, CreatedAt: createdAt,
		PayloadBytes: size, PayloadSHA256: hex.EncodeToString(hasher.Sum(nil)),
	}
	encoded, err := EncodeHeader(header)
	if err != nil {
		_ = file.Close()
		return nil, exportFailed()
	}
	keep = true
	return &Export{
		Name: FileName(createdAt, version), Header: header, Size: int64(len(encoded)) + size,
		encoded: encoded, payload: file, dir: dir,
	}, nil
}

// Restore validates an uploaded backup and loads it into a new database next
// to the live one. Nothing the app serves changes here: every refusal leaves
// the current data as it was. On success the loaded database is marked ready
// and HandoffRequested is closed; the switch happens in Activate.
func (s *Service) Restore(ctx context.Context, body io.Reader) (RestoreAccepted, error) {
	if reason := s.restoreBlocker(ctx); reason != "" {
		if reason == ReasonRestoreInProgress {
			return RestoreAccepted{}, restoreInProgress()
		}
		return RestoreAccepted{}, &apperrors.Business{Status: 409, Code: "RESTORE_UNAVAILABLE", Message: restoreUnavailableText[reason]}
	}
	if !s.begin() {
		return RestoreAccepted{}, restoreInProgress()
	}
	handedOff := false
	defer func() {
		if !handedOff {
			s.end()
		}
	}()

	running, err := s.runningSchema(ctx)
	if err != nil {
		return RestoreAccepted{}, err
	}
	reader := bufio.NewReaderSize(body, 64<<10)
	header, err := ReadHeader(reader)
	if err != nil {
		return RestoreAccepted{}, fileInvalid()
	}
	if header.SchemaVersion != running {
		return RestoreAccepted{}, schemaMismatch(header.SchemaVersion, running)
	}
	if header.PayloadBytes > MaxPayloadBytes {
		return RestoreAccepted{}, &apperrors.Business{Status: 422, Code: "BACKUP_FILE_TOO_LARGE", Message: "The backup file is larger than a restore accepts."}
	}

	dir, err := os.MkdirTemp(s.tempDir, "bansoko-restore-*")
	if err != nil {
		return RestoreAccepted{}, fmt.Errorf("create restore workspace: %w", err)
	}
	defer os.RemoveAll(dir)
	payload := filepath.Join(dir, "payload.dump")
	if err := receivePayload(reader, payload, header); err != nil {
		if errors.Is(err, ErrInvalidFile) {
			return RestoreAccepted{}, fileInvalid()
		}
		return RestoreAccepted{}, err
	}
	if err := s.tools.Verify(ctx, payload); err != nil {
		return RestoreAccepted{}, fileInvalid()
	}

	database := s.cluster.Database()
	stamp := s.now().Unix()
	loading := sideName(database, kindRestoring, stamp)
	admin, err := s.cluster.Open(ctx, maintenanceDatabase)
	if err != nil {
		return RestoreAccepted{}, fmt.Errorf("connect to the database server: %w", err)
	}
	defer admin.Close()
	if err := createLike(ctx, admin, loading, database); err != nil {
		return RestoreAccepted{}, fmt.Errorf("create restore database: %w", err)
	}
	created := loading
	defer func() {
		if created != "" {
			// The request may be gone; the cleanup still runs.
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if err := dropDatabase(cleanupCtx, admin, created); err != nil {
				s.warn(cleanupCtx, "restore", "backup_restore_cleanup_failed", "db")
			}
		}
	}()
	if err := s.tools.RestoreInto(ctx, payload, loading); err != nil {
		s.warn(ctx, "restore", "backup_restore_load_failed", "load")
		return RestoreAccepted{}, &apperrors.Business{Status: 422, Code: "BACKUP_LOAD_FAILED", Message: "The backup could not be loaded. Current data is unchanged."}
	}
	if err := s.prepareLoaded(ctx, loading, running); err != nil {
		return RestoreAccepted{}, err
	}
	ready := sideName(database, kindRestored, stamp)
	if err := renameDatabase(ctx, admin, loading, ready); err != nil {
		return RestoreAccepted{}, fmt.Errorf("mark restored database ready: %w", err)
	}
	created = ready
	if ctx.Err() != nil {
		// The upload request ended before the answer: do not switch data the
		// owner was never told about.
		return RestoreAccepted{}, ctx.Err()
	}
	created = ""
	s.mu.Lock()
	s.ready = ready
	s.mu.Unlock()
	handedOff = true
	s.logger.Warn(ctx, observabilityport.RuntimeEvent{
		Message: "backup restore verified; restarting to switch to the restored data", Component: "backup",
		Operation: "restore", Outcome: "warning", ReasonCode: "backup_restore_ready",
	})
	close(s.handoff)
	return RestoreAccepted{BackupCreatedAt: header.CreatedAt}, nil
}

// prepareLoaded proves the loaded database is exactly the running schema
// (identity, every migration with its checksum, the relation set) and signs
// everyone out of it: sessions in a backup must not outlive the restore.
func (s *Service) prepareLoaded(ctx context.Context, loading, running string) error {
	loaded, err := s.cluster.Open(ctx, loading)
	if err != nil {
		return fmt.Errorf("connect to restore database: %w", err)
	}
	defer loaded.Close()
	version, err := latestSchemaVersion(ctx, loaded)
	if err != nil || version != running {
		return schemaMismatch(version, running)
	}
	if err := (migrations.Runner{DB: loaded, Dir: s.schemaDir}).Verify(ctx); err != nil {
		return schemaMismatch(version, running)
	}
	if _, err := loaded.ExecContext(ctx, `DELETE FROM auth_sessions`); err != nil {
		return fmt.Errorf("sign out restored sessions: %w", err)
	}
	return nil
}

// receivePayload stores exactly header.PayloadBytes bytes and proves their
// SHA-256 and that nothing follows them.
func receivePayload(r io.Reader, path string, header Header) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create restore payload: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	copied, err := io.CopyN(io.MultiWriter(file, hasher), r, header.PayloadBytes)
	if err != nil || copied != header.PayloadBytes {
		return fmt.Errorf("%w: payload is shorter than its header says", ErrInvalidFile)
	}
	var extra [1]byte
	if n, _ := io.ReadFull(r, extra[:]); n != 0 {
		return fmt.Errorf("%w: data follows the payload", ErrInvalidFile)
	}
	if hex.EncodeToString(hasher.Sum(nil)) != header.PayloadSHA256 {
		return fmt.Errorf("%w: payload checksum does not match", ErrInvalidFile)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("store restore payload: %w", err)
	}
	return nil
}

func (s *Service) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restoring || s.ready != "" {
		return false
	}
	s.restoring = true
	return true
}

func (s *Service) end() {
	s.mu.Lock()
	s.restoring = false
	s.mu.Unlock()
}

// Activate switches the deployment database to the ready restored database.
// It runs after the serving pool is closed and the background runtime is
// stopped. The live database is renamed aside, the restored one takes its
// name, and the old one is dropped; a failure before the restored database
// has the name puts the live database back, so the data is either fully
// switched or unchanged. A crash in between is finished by
// RecoverInterrupted at the next start.
func (s *Service) Activate(ctx context.Context) error {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if ready == "" {
		return errors.New("no restored database is ready")
	}
	admin, err := s.cluster.Open(ctx, maintenanceDatabase)
	if err != nil {
		return fmt.Errorf("connect to the database server: %w", err)
	}
	defer admin.Close()
	_, stamp, _ := parseSideName(s.cluster.Database(), ready)
	err = swapIn(ctx, admin, s.cluster.Database(), ready, stamp)
	if err != nil {
		s.warn(ctx, "restore", "backup_restore_switch_failed", "db")
		return err
	}
	s.logger.Info(ctx, observabilityport.RuntimeEvent{
		Message: "database restored from backup", Component: "backup", Operation: "restore",
		Outcome: "success", ReasonCode: "backup_restored",
	})
	return nil
}

// Abandon drops the ready restored database when the handoff could not stop
// the app cleanly; the current data stays.
func (s *Service) Abandon(ctx context.Context) error {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if ready == "" {
		return nil
	}
	admin, err := s.cluster.Open(ctx, maintenanceDatabase)
	if err != nil {
		return err
	}
	defer admin.Close()
	return dropDatabase(ctx, admin, ready)
}

func (s *Service) warn(ctx context.Context, operation, reason, class string) {
	s.logger.Warn(ctx, observabilityport.RuntimeEvent{
		Message: "backup " + operation + " failed", Component: "backup", Operation: operation,
		Outcome: "failure", ReasonCode: reason, ErrorClass: class,
	})
}

var restoreUnavailableText = map[string]string{
	ReasonToolsUnavailable:       "The database backup tools are not available on this deployment.",
	ReasonDatabaseUnsupported:    "This deployment's database connection does not support restoring.",
	ReasonPrivilegesInsufficient: "The database owner role may not create and rename databases.",
}

func restoreInProgress() error {
	return &apperrors.Business{Status: 409, Code: "RESTORE_IN_PROGRESS", Message: "A restore is in progress."}
}

func exportFailed() error {
	return &apperrors.Business{Status: 503, Code: "BACKUP_EXPORT_FAILED", Message: "The database could not be exported."}
}

func fileInvalid() error {
	return &apperrors.Business{Status: 422, Code: "BACKUP_FILE_INVALID", Message: "The file is not a valid backup of this app. Current data is unchanged."}
}

func schemaMismatch(backup, running string) error {
	backupNumber, _ := SchemaNumber(backup)
	runningNumber, _ := SchemaNumber(running)
	return &apperrors.Business{Status: 422, Code: "BACKUP_SCHEMA_MISMATCH", Message: fmt.Sprintf(
		"The backup belongs to data version %s; this app runs version %s. Current data is unchanged.",
		orUnknown(backupNumber), orUnknown(runningNumber))}
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
