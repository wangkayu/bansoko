package localupdate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/redact"
	updateapplication "bansoko/internal/update/application"
	updatetransport "bansoko/internal/update/transport"
)

const (
	maxRequestBytes       = 64 << 10
	defaultMaxConnections = 8
	defaultReadTimeout    = 5 * time.Second
	defaultWriteTimeout   = 5 * time.Second
	defaultCloseGrace     = 5 * time.Second
	defaultCloseTimeout   = 5 * time.Second
)

// Options bounds the local listener. Zero values select the safe defaults.
type Options struct {
	MaxConnections int
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	// CloseGrace lets an admitted apply flush its response before shutdown.
	// CloseTimeout bounds the subsequent cancel/close phase for stuck requests.
	CloseGrace   time.Duration
	CloseTimeout time.Duration
	Logger       observabilityport.RuntimeLogger

	// Package-local seams are fixed before the accept goroutine starts. They let
	// tests exercise kernel-peer refusal and the dispatch/write handoff boundary.
	peerIdentity func(*net.UnixConn) (uint32, error)
	beforeWrite  func()
}

type requestState struct {
	cancel        context.CancelFunc
	applyRequest  bool
	responseReady bool
}

// Server owns only the socket and its in-flight local requests. Update truth
// remains in the application service, pending marker, journal and DB fence.
type Server struct {
	path         string
	listener     *net.UnixListener
	service      Service
	logger       observabilityport.RuntimeLogger
	socketInfo   os.FileInfo
	readTimeout  time.Duration
	writeTimeout time.Duration
	closeGrace   time.Duration
	closeTimeout time.Duration
	slots        chan struct{}
	closed       chan struct{}
	acceptDone   chan struct{}
	peerIdentity func(*net.UnixConn) (uint32, error)
	beforeWrite  func()

	closeOnce sync.Once
	mu        sync.Mutex
	closing   bool
	conns     map[net.Conn]*requestState
	handlers  sync.WaitGroup
}

// SocketPath returns the single local control endpoint below the configured
// updates root. The CLI deliberately has no independent endpoint setting.
func SocketPath(updatesRoot string) string {
	return filepath.Join(updatesRoot, socketFileName)
}

// Listen verifies the directory chain and binds the local control socket. An
// active listener, an unsafe parent, or an unexpected probe error is never
// treated as a stale socket.
func Listen(updatesRoot string, service Service, options Options) (_ *Server, resultErr error) {
	if !Supported {
		return nil, errors.New("local update control is unsupported on this platform")
	}
	if service == nil {
		return nil, errors.New("local update control requires the running embedded update service")
	}
	root, err := ensureDirectoryChain(updatesRoot)
	if err != nil {
		return nil, fmt.Errorf("local update parent directory is unsafe: %w", err)
	}
	path := SocketPath(root)
	if err := removeProvenStaleSocket(path); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen for local update control: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	createdInfo, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("inspect new local update socket: %w", err)
	}
	cleanup := true
	defer func() {
		if resultErr != nil && cleanup {
			_ = listener.Close()
			_ = removeSocketIfSame(path, createdInfo)
		}
	}()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict local update socket: %w", err)
	}
	if createdInfo.Mode()&os.ModeSocket == 0 {
		return nil, errors.New("local update endpoint is not a socket")
	}
	info := createdInfo
	maxConnections := options.MaxConnections
	if maxConnections == 0 {
		maxConnections = defaultMaxConnections
	}
	if maxConnections < 1 || maxConnections > 64 {
		return nil, errors.New("local update connection limit must be between 1 and 64")
	}
	readTimeout := options.ReadTimeout
	if readTimeout == 0 {
		readTimeout = defaultReadTimeout
	}
	writeTimeout := options.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultWriteTimeout
	}
	if readTimeout < 0 || writeTimeout < 0 {
		return nil, errors.New("local update connection timeouts must not be negative")
	}
	closeGrace := options.CloseGrace
	if closeGrace == 0 {
		closeGrace = defaultCloseGrace
	}
	closeTimeout := options.CloseTimeout
	if closeTimeout == 0 {
		closeTimeout = defaultCloseTimeout
	}
	if closeGrace < 0 || closeTimeout < 0 {
		return nil, errors.New("local update close timeouts must not be negative")
	}
	logger := options.Logger
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	identity := options.peerIdentity
	if identity == nil {
		identity = peerUID
	}
	server := &Server{
		path: path, listener: listener, service: service, logger: logger, socketInfo: info,
		readTimeout: readTimeout, writeTimeout: writeTimeout, closeGrace: closeGrace, closeTimeout: closeTimeout,
		slots: make(chan struct{}, maxConnections), closed: make(chan struct{}), acceptDone: make(chan struct{}),
		peerIdentity: identity, beforeWrite: options.beforeWrite, conns: make(map[net.Conn]*requestState),
	}
	cleanup = false
	go server.serve()
	return server, nil
}

// ensureDirectoryChain canonicalizes existing ancestors (Darwin's /var is
// itself a symlink), creates only missing descendants below that canonical
// parent, and refuses a final updates-root symlink. The final directory must be
// owned by the serving identity and must not be writable by group or others.
func ensureDirectoryChain(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("updates root is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	parent, err := canonicalParent(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	absolute = filepath.Join(parent, filepath.Base(absolute))
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		if mkdirErr := os.Mkdir(absolute, 0o700); mkdirErr != nil {
			return "", fmt.Errorf("create %s: %w", absolute, mkdirErr)
		}
		info, err = os.Lstat(absolute)
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s is not a real directory", absolute)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%s is group- or world-writable", absolute)
	}
	owner, err := fileOwnerUID(info)
	if err != nil {
		return "", err
	}
	if owner != uint32(os.Geteuid()) {
		return "", fmt.Errorf("%s is owned by uid %d, not serving uid %d", absolute, owner, os.Geteuid())
	}
	return absolute, nil
}

func canonicalParent(parent string) (string, error) {
	current := parent
	missing := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			info, statErr := os.Lstat(resolved)
			if statErr != nil || !info.IsDir() {
				return "", fmt.Errorf("update parent %s is not a directory", resolved)
			}
			for _, component := range missing {
				resolved = filepath.Join(resolved, component)
				if err := os.Mkdir(resolved, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
					return "", fmt.Errorf("create update parent %s: %w", resolved, err)
				}
				info, err := os.Lstat(resolved)
				if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
					return "", fmt.Errorf("update parent %s is not a real directory", resolved)
				}
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(current)
		if next == current {
			return "", fmt.Errorf("no existing parent for %s", parent)
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = next
	}
}

func removeProvenStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local update socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("local update path %s exists and is not a socket; refusing to replace it", path)
	}
	connection, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("local update socket %s already has an active listener", path)
	}
	// Only an immediate refusal or disappearance proves that no listener owns
	// the socket. Timeout, permission and every other error fail closed.
	if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
		return fmt.Errorf("probe active local update listener: %w", dialErr)
	}
	current, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return statErr
	}
	if current.Mode()&os.ModeSocket == 0 || !os.SameFile(info, current) {
		return errors.New("local update socket changed while checking it; refusing to unlink")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale local update socket: %w", err)
	}
	return nil
}

func (s *Server) serve() {
	defer close(s.acceptDone)
	for {
		connection, err := s.listener.AcceptUnix()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
			}
			s.logger.Warn(context.Background(), observabilityport.RuntimeEvent{
				Message: "local update accept failed", Component: "update", Operation: "local_accept",
				Outcome: "warning", ReasonCode: "update_local_accept_failed", ErrorClass: "update_local",
			})
			continue
		}
		select {
		case s.slots <- struct{}{}:
			s.handlers.Add(1)
			go s.handle(connection)
		default:
			// A full bounded worker set sheds the connection before request
			// authentication or application work; no unbounded handler goroutines.
			_ = connection.Close()
		}
	}
}

func (s *Server) handle(connection *net.UnixConn) {
	defer s.handlers.Done()
	defer func() { <-s.slots }()
	defer connection.Close()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	state := &requestState{cancel: cancelRequest}
	s.conns[connection] = state
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, connection)
		s.mu.Unlock()
	}()

	uid, err := s.peerIdentity(connection)
	if err != nil {
		s.write(connection, response{Error: &responseError{Status: 403, Code: "UPDATE_LOCAL_PERMISSION", Message: "The local peer identity could not be verified."}})
		return
	}
	if !authorizedUID(uid, uint32(os.Geteuid())) {
		s.write(connection, response{Error: &responseError{Status: 403, Code: "UPDATE_LOCAL_PERMISSION", Message: "Local update control requires root or the serving PUID."}})
		return
	}
	if err := connection.SetReadDeadline(time.Now().Add(s.readTimeout)); err != nil {
		return
	}
	req, err := readRequest(connection)
	if err != nil {
		s.write(connection, response{Error: &responseError{Status: 400, Code: "UPDATE_LOCAL_REQUEST_INVALID", Message: "The local request is invalid, too large, or was not received before the read deadline."}})
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	s.mu.Lock()
	state.applyRequest = req.Action == "apply"
	s.mu.Unlock()
	result := s.dispatch(requestCtx, req)
	s.mu.Lock()
	state.responseReady = true
	s.mu.Unlock()
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	s.write(connection, result)
}

func readRequest(reader io.Reader) (request, error) {
	limited := &io.LimitedReader{R: reader, N: maxRequestBytes + 1}
	line, err := bufio.NewReader(limited).ReadBytes('\n')
	if err != nil {
		return request{}, err
	}
	if len(line) > maxRequestBytes {
		return request{}, errors.New("request exceeds the local size bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var req request
	if err := decoder.Decode(&req); err != nil {
		return request{}, err
	}
	if decoder.More() {
		return request{}, errors.New("multiple JSON documents")
	}
	if req.Version != protocolVersion {
		return request{}, errors.New("unsupported local protocol")
	}
	return req, nil
}

func (s *Server) dispatch(ctx context.Context, req request) response {
	switch req.Action {
	case "check":
		result, err := s.service.CheckUpdate(ctx)
		if err != nil {
			return response{Error: mapError(err)}
		}
		mapped := updatetransport.Check(result)
		return response{Check: &mapped}
	case "apply":
		if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 256 {
			return response{Error: &responseError{Status: 400, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "A bounded idempotency key is required."}}
		}
		checked, err := s.service.CheckUpdate(ctx)
		if err != nil {
			return response{Error: mapError(err)}
		}
		if checked.ActiveOperation != nil {
			return response{Error: &responseError{Status: 409, Code: "UPDATE_LOCKED", Message: "An update is already in progress."}}
		}
		if checked.Candidate == nil {
			return response{Error: &responseError{Status: 422, Code: "UPDATE_TARGET_CURRENT", Message: "The running release is already current, or no candidate is available."}}
		}
		status, _, err := s.service.AdmitUpdate(ctx, updateAdmitInput(*checked.Candidate), req.IdempotencyKey)
		if err != nil {
			return response{Error: mapError(err)}
		}
		mapped := updatetransport.Status(status)
		return response{Status: &mapped}
	case "status":
		status, found, err := s.service.GetUpdateStatus(ctx)
		if err != nil {
			return response{Error: mapError(err)}
		}
		if !found {
			return response{Error: &responseError{Status: 404, Code: "UPDATE_STATUS_NOT_FOUND", Message: "No update operation has been recorded."}}
		}
		mapped := updatetransport.Status(status)
		return response{Status: &mapped}
	default:
		return response{Error: &responseError{Status: 400, Code: "UPDATE_LOCAL_REQUEST_INVALID", Message: "The local update action is unsupported."}}
	}
}

func updateAdmitInput(candidate updateapplication.Candidate) updateapplication.AdmitInput {
	return updateapplication.AdmitInput{TargetReleaseID: candidate.ReleaseID, ManifestDigest: candidate.ManifestDigest}
}

func mapError(err error) *responseError {
	var business *apperrors.Business
	if errors.As(err, &business) {
		status := business.Status
		if status != 409 && status != 422 && status != 503 {
			status = 409
		}
		return &responseError{Status: status, Code: business.Code, Message: redact.URLsInText(business.Message)}
	}
	var badRequest *apperrors.BadRequest
	if errors.As(err, &badRequest) {
		return &responseError{Status: 400, Code: badRequest.Code, Message: redact.URLsInText(badRequest.Message)}
	}
	var notFound *apperrors.NotFound
	if errors.As(err, &notFound) {
		return &responseError{Status: 404, Code: "NOT_FOUND", Message: "The requested update state was not found."}
	}
	var imageCompatibility *apperrors.ImageCompatibilityError
	if errors.As(err, &imageCompatibility) {
		return &responseError{Status: 409, Code: "UPDATE_REQUIRES_IMAGE", Message: "This update requires a newer base image. Update the image channel first."}
	}
	var bootstrapTooOld *apperrors.BootstrapTooOldError
	if errors.As(err, &bootstrapTooOld) {
		return &responseError{Status: 409, Code: "BOOTSTRAP_TOO_OLD", Message: "This update requires a newer bootstrap in the base image. Update the image channel first."}
	}
	return &responseError{Status: 500, Code: "INTERNAL", Message: "The update request failed."}
}

func (s *Server) write(connection net.Conn, value response) {
	_ = connection.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	_ = json.NewEncoder(connection).Encode(value)
}

// Close first stops accepts and gives in-flight requests a bounded grace
// period. It cancels unfinished application calls without closing transports
// that can still return an admitted apply or an already-generated response.
// Handler waiting and socket unlinking remain bounded and inode-safe.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var result error
	s.closeOnce.Do(func() {
		var closeErrors []error
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		close(s.closed)
		if closeErr := s.listener.Close(); closeErr != nil {
			closeErrors = append(closeErrors, closeErr)
		}
		<-s.acceptDone
		if !s.waitHandlers(s.closeGrace) {
			s.mu.Lock()
			for connection, state := range s.conns {
				if state.responseReady {
					// Admission has already returned to the transport. Never truncate
					// that response by forcing this connection closed; its write still
					// has the normal write deadline and Close remains bounded.
					continue
				}
				state.cancel()
				if state.applyRequest {
					// AdmitUpdate starts runChain before it returns. If that chain has
					// already requested handoff, closing this socket now would erase an
					// accepted response before dispatch can mark it ready. Cancel the
					// request context, but leave the transport alive for that return;
					// waitHandlers below still imposes the same hard upper bound.
					continue
				}
				_ = connection.Close()
			}
			s.mu.Unlock()
			if !s.waitHandlers(s.closeTimeout) {
				closeErrors = append(closeErrors, errors.New("local update handlers did not stop before the close deadline"))
			}
		}
		if removeErr := removeSocketIfSame(s.path, s.socketInfo); removeErr != nil {
			closeErrors = append(closeErrors, removeErr)
		}
		result = errors.Join(closeErrors...)
	})
	return result
}

// waitHandlers is called only after the accept loop has stopped, so no further
// WaitGroup Add can race with Wait. A false result leaves no unbounded Close wait.
func (s *Server) waitHandlers(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.handlers.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func removeSocketIfSame(path string, expected os.FileInfo) error {
	if expected == nil {
		return nil
	}
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSocket == 0 || !os.SameFile(expected, current) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func authorizedUID(peer, serving uint32) bool {
	return peer == 0 || peer == serving
}
