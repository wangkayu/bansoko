package localupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	updateapplication "bansoko/internal/update/application"
	updatetransport "bansoko/internal/update/transport"
)

type fakeUpdateService struct {
	checkFunc  func(context.Context) (updateapplication.Check, error)
	admitFunc  func(context.Context, updateapplication.AdmitInput, string) (updateapplication.Status, bool, error)
	statusFunc func(context.Context) (updateapplication.Status, bool, error)

	mu          sync.Mutex
	admitInput  updateapplication.AdmitInput
	admitKey    string
	statusCalls int
}

func newFakeUpdateService() *fakeUpdateService {
	return &fakeUpdateService{
		checkFunc: func(context.Context) (updateapplication.Check, error) {
			return updateapplication.Check{
				Status:        "update_available",
				Current:       updateapplication.Current{ReleaseID: "release-1", Version: "1.0.0", ImageCompatibility: "base-v1"},
				Candidate:     &updateapplication.Candidate{ReleaseID: "release-2", Version: "1.1.0", ManifestDigest: "sha256:candidate"},
				Compatibility: updateapplication.Compatibility{Kind: "overlay", Reason: "compatible"},
			}, nil
		},
		admitFunc: func(_ context.Context, _ updateapplication.AdmitInput, _ string) (updateapplication.Status, bool, error) {
			return fakeStatus("accepted"), true, nil
		},
		statusFunc: func(context.Context) (updateapplication.Status, bool, error) {
			return fakeStatus("accepted"), true, nil
		},
	}
}

func fakeStatus(step string) updateapplication.Status {
	version := "1.1.0"
	return updateapplication.Status{OperationID: "operation-1", Channel: "embedded", Step: step, TargetReleaseID: "release-2", TargetVersion: &version, UpdatedAt: time.Unix(100, 0).UTC()}
}

func (f *fakeUpdateService) CheckUpdate(ctx context.Context) (updateapplication.Check, error) {
	return f.checkFunc(ctx)
}

func (f *fakeUpdateService) AdmitUpdate(ctx context.Context, input updateapplication.AdmitInput, key string) (updateapplication.Status, bool, error) {
	f.mu.Lock()
	f.admitInput, f.admitKey = input, key
	f.mu.Unlock()
	return f.admitFunc(ctx, input, key)
}

func (f *fakeUpdateService) GetUpdateStatus(ctx context.Context) (updateapplication.Status, bool, error) {
	f.mu.Lock()
	f.statusCalls++
	f.mu.Unlock()
	return f.statusFunc(ctx)
}

func startTestServer(t *testing.T, root string, service Service, options Options) *Server {
	t.Helper()
	server, err := Listen(root, service, options)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return server
}

func TestLocalCommandsReuseRunningService(t *testing.T) {
	service := newFakeUpdateService()
	root := testUpdatesRoot(t)
	server := startTestServer(t, root, service, Options{})
	client := &localClient{path: SocketPath(root)}

	checked, callErr := client.call("check", "", time.Second)
	if callErr != nil {
		t.Fatalf("check error = %v", callErr)
	}
	if checked.Check == nil || checked.Check.Status != "update_available" || checked.Check.Candidate.ReleaseId != "release-2" {
		t.Fatalf("unexpected check response: %+v", checked.Check)
	}
	admitted, callErr := client.call("apply", "request-1", time.Second)
	if callErr != nil {
		t.Fatalf("apply error = %v", callErr)
	}
	if admitted.Status == nil || admitted.Status.Step != "accepted" || admitted.Status.OperationId != "operation-1" {
		t.Fatalf("unexpected apply response: %+v", admitted.Status)
	}
	service.mu.Lock()
	input, key := service.admitInput, service.admitKey
	service.mu.Unlock()
	if input.TargetReleaseID != "release-2" || input.ManifestDigest != "sha256:candidate" || key != "request-1" {
		t.Fatalf("admit input = %+v key %q", input, key)
	}
	service.statusFunc = func(context.Context) (updateapplication.Status, bool, error) { return fakeStatus("failed"), true, nil }
	status, callErr := client.call("status", "", time.Second)
	if callErr != nil || status.Status.Step != "failed" {
		t.Fatalf("status = %+v, %v", status.Status, callErr)
	}
	info, err := os.Lstat(server.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRunJSONOutputAndExitCodes(t *testing.T) {
	t.Run("check JSON is one authoritative document", func(t *testing.T) {
		service := newFakeUpdateService()
		root := testUpdatesRoot(t)
		startTestServer(t, root, service, Options{})
		var stdout, stderr bytes.Buffer
		code := Run([]string{"check", "--json"}, testEnvironment(root), &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("code %d stderr %q", code, stderr.String())
		}
		var document map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
			t.Fatalf("invalid JSON %q: %v", stdout.String(), err)
		}
		if document["status"] != "update_available" || document["error"] != nil {
			t.Fatalf("document = %v", document)
		}
	})

	t.Run("missing status", func(t *testing.T) {
		service := newFakeUpdateService()
		service.statusFunc = func(context.Context) (updateapplication.Status, bool, error) {
			return updateapplication.Status{}, false, nil
		}
		root := testUpdatesRoot(t)
		startTestServer(t, root, service, Options{})
		var stdout, stderr bytes.Buffer
		code := Run([]string{"status", "--json"}, testEnvironment(root), &stdout, &stderr)
		assertErrorDocument(t, code, stdout.String(), 4, "UPDATE_STATUS_NOT_FOUND")
	})

	t.Run("active apply conflict", func(t *testing.T) {
		service := newFakeUpdateService()
		service.checkFunc = func(context.Context) (updateapplication.Check, error) {
			active := fakeStatus("accepted")
			return updateapplication.Check{ActiveOperation: &active}, nil
		}
		root := testUpdatesRoot(t)
		startTestServer(t, root, service, Options{})
		var stdout, stderr bytes.Buffer
		code := Run([]string{"apply", "--json"}, testEnvironment(root), &stdout, &stderr)
		assertErrorDocument(t, code, stdout.String(), 5, "UPDATE_LOCKED")
	})

	t.Run("terminal status is a successful query", func(t *testing.T) {
		service := newFakeUpdateService()
		service.statusFunc = func(context.Context) (updateapplication.Status, bool, error) {
			return fakeStatus("rolled_back"), true, nil
		}
		root := testUpdatesRoot(t)
		startTestServer(t, root, service, Options{})
		var stdout, stderr bytes.Buffer
		code := Run([]string{"status"}, testEnvironment(root), &stdout, &stderr)
		if code != 0 || !strings.Contains(stdout.String(), "status query succeeded") {
			t.Fatalf("code %d stdout %q", code, stdout.String())
		}
	})

	t.Run("manual mode and unavailable listener", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"check", "--json"}, func(key string) string {
			if key == "BANSOKO_UPDATE_MODE" {
				return "manual"
			}
			return ""
		}, &stdout, &stderr)
		assertErrorDocument(t, code, stdout.String(), 6, "SERVICE_NOT_CONFIGURED")
		stdout.Reset()
		code = Run([]string{"check", "--json"}, testEnvironment(filepath.Join(t.TempDir(), "missing")), &stdout, &stderr)
		assertErrorDocument(t, code, stdout.String(), 6, "UPDATE_LOCAL_UNAVAILABLE")
	})

	t.Run("usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"status", "--wait", "--json"}, testEnvironment(t.TempDir()), &stdout, &stderr)
		assertErrorDocument(t, code, stdout.String(), 2, "USAGE")
	})
}

func TestApplyDefaultReturnsAtAdmission(t *testing.T) {
	service := newFakeUpdateService()
	root := testUpdatesRoot(t)
	startTestServer(t, root, service, Options{})
	var stdout, stderr bytes.Buffer
	code := Run([]string{"apply"}, testEnvironment(root), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "accepted, not completed") {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	service.mu.Lock()
	calls := service.statusCalls
	service.mu.Unlock()
	if calls != 0 {
		t.Fatalf("default apply polled status %d times", calls)
	}
}

func TestApplyWaitTerminalOutcomes(t *testing.T) {
	for _, test := range []struct {
		step     string
		wantCode int
	}{
		{step: "succeeded", wantCode: 0},
		{step: "failed", wantCode: 1},
		{step: "rolled_back", wantCode: 1},
		{step: "rollback_blocked", wantCode: 1},
	} {
		t.Run(test.step, func(t *testing.T) {
			service := newFakeUpdateService()
			service.statusFunc = func(context.Context) (updateapplication.Status, bool, error) { return fakeStatus(test.step), true, nil }
			root := testUpdatesRoot(t)
			startTestServer(t, root, service, Options{})
			oldInterval := waitPollInterval
			waitPollInterval = time.Millisecond
			t.Cleanup(func() { waitPollInterval = oldInterval })
			var stdout, stderr bytes.Buffer
			code := Run([]string{"apply", "--wait", "--wait-timeout", "1s", "--json"}, testEnvironment(root), &stdout, &stderr)
			if code != test.wantCode {
				t.Fatalf("code %d, want %d; stdout %q stderr %q", code, test.wantCode, stdout.String(), stderr.String())
			}
			var document map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &document); err != nil || document["step"] != test.step {
				t.Fatalf("document %v error %v", document, err)
			}
			if test.wantCode != 0 && !strings.Contains(stderr.String(), "not a successful update") {
				t.Fatalf("terminal explanation missing: %q", stderr.String())
			}
		})
	}
}

func TestApplyWaitTimeoutIsUnknown(t *testing.T) {
	service := newFakeUpdateService()
	root := testUpdatesRoot(t)
	startTestServer(t, root, service, Options{})
	oldInterval := waitPollInterval
	waitPollInterval = time.Millisecond
	t.Cleanup(func() { waitPollInterval = oldInterval })
	var stdout, stderr bytes.Buffer
	code := Run([]string{"apply", "--wait", "--wait-timeout", "5ms", "--json"}, testEnvironment(root), &stdout, &stderr)
	assertErrorDocument(t, code, stdout.String(), 7, "UPDATE_WAIT_TIMEOUT")
	if !strings.Contains(stdout.String(), "unknown, not failed") {
		t.Fatalf("timeout is not explained as unknown: %q", stdout.String())
	}
}

func TestApplyRequestTimeoutDoesNotClaimFailure(t *testing.T) {
	service := newFakeUpdateService()
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	service.checkFunc = func(context.Context) (updateapplication.Check, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return updateapplication.Check{Status: "up_to_date"}, nil
	}
	defer releaseOnce.Do(func() { close(release) })
	root := testUpdatesRoot(t)
	startTestServer(t, root, service, Options{})
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"apply", "--timeout", "20ms", "--json"}, testEnvironment(root), &stdout, &stderr)
	}()
	<-started
	code := <-done
	assertErrorDocument(t, code, stdout.String(), 6, "UPDATE_LOCAL_TIMEOUT")
	if !strings.Contains(stdout.String(), "Admission is unknown, not failed") {
		t.Fatalf("apply timeout claims a definite outcome: %q", stdout.String())
	}
	releaseOnce.Do(func() { close(release) })
}

func TestHandoffBeforeAdmitDispatchReturnsPreservesAcceptedResponse(t *testing.T) {
	service := newFakeUpdateService()
	admitStarted := make(chan struct{})
	exitRequested := make(chan struct{})
	requestCanceled := make(chan struct{})
	allowAdmitReturn := make(chan struct{})
	admitContextErr := make(chan error, 1)
	service.admitFunc = func(ctx context.Context, _ updateapplication.AdmitInput, _ string) (updateapplication.Status, bool, error) {
		close(admitStarted)
		// This is the exact ordering admitted by Service.admit: it starts
		// go s.runChain(...) before returning. Here the equivalent runChain wins
		// the scheduler and requests main's handoff while dispatch is still blocked.
		go func() { close(exitRequested) }()
		<-ctx.Done()
		close(requestCanceled)
		<-allowAdmitReturn
		admitContextErr <- ctx.Err()
		return fakeStatus("accepted"), true, nil
	}
	root := testUpdatesRoot(t)
	server := startTestServer(t, root, service, Options{CloseGrace: 20 * time.Millisecond, CloseTimeout: time.Second})
	client := &localClient{path: SocketPath(root)}
	type callResult struct {
		response response
		err      *commandError
	}
	callDone := make(chan callResult, 1)
	go func() {
		result, err := client.call("apply", "request-early-handoff", time.Second)
		callDone <- callResult{response: result, err: err}
	}()
	<-admitStarted
	closeDone := make(chan error, 1)
	go func() {
		<-exitRequested
		closeDone <- server.Close()
	}()
	select {
	case <-requestCanceled:
	case err := <-closeDone:
		t.Fatalf("handoff Close finished before AdmitUpdate returned: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the unfinished admission")
	}
	close(allowAdmitReturn)
	result := <-callDone
	if result.err != nil || result.response.Status == nil || result.response.Status.OperationId != "operation-1" || result.response.Status.Step != "accepted" {
		t.Fatalf("early handoff truncated accepted response: %+v, %v", result.response, result.err)
	}
	if err := <-admitContextErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("unfinished apply context was not canceled during Close: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close after AdmitUpdate returned: %v", err)
	}
}

func TestCloseFlushesAcceptedApplyBeforeHandoff(t *testing.T) {
	service := newFakeUpdateService()
	root := testUpdatesRoot(t)
	dispatched := make(chan struct{})
	allowWrite := make(chan struct{})
	server := startTestServer(t, root, service, Options{CloseGrace: 20 * time.Millisecond, CloseTimeout: time.Second, beforeWrite: func() {
		close(dispatched)
		<-allowWrite
	}})
	client := &localClient{path: SocketPath(root)}
	type callResult struct {
		response response
		err      *commandError
	}
	callDone := make(chan callResult, 1)
	go func() {
		result, err := client.call("apply", "request-handoff", time.Second)
		callDone <- callResult{response: result, err: err}
	}()
	<-dispatched
	closeDone := make(chan error, 1)
	go func() { closeDone <- server.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close finished before the accepted response was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(allowWrite)
	result := <-callDone
	if result.err != nil || result.response.Status == nil || result.response.Status.OperationId != "operation-1" || result.response.Status.Step != "accepted" {
		t.Fatalf("accepted response was truncated by Close: %+v, %v", result.response, result.err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close after accepted response: %v", err)
	}
}

func TestCloseCancelsRequestsAndBoundsHandlers(t *testing.T) {
	t.Run("application context is canceled", func(t *testing.T) {
		service := newFakeUpdateService()
		started := make(chan struct{})
		canceled := make(chan struct{})
		service.checkFunc = func(ctx context.Context) (updateapplication.Check, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return updateapplication.Check{}, ctx.Err()
		}
		root := testUpdatesRoot(t)
		server := startTestServer(t, root, service, Options{CloseGrace: 10 * time.Millisecond, CloseTimeout: time.Second})
		client := &localClient{path: SocketPath(root)}
		callDone := make(chan *commandError, 1)
		go func() {
			_, err := client.call("check", "", time.Second)
			callDone <- err
		}()
		<-started
		if err := server.Close(); err != nil {
			t.Fatalf("Close with cooperative cancellation: %v", err)
		}
		select {
		case <-canceled:
		default:
			t.Fatal("application request context was not canceled")
		}
		if err := <-callDone; err == nil {
			t.Fatal("canceled client unexpectedly received a successful response")
		}
	})

	t.Run("uncooperative application cannot block Close", func(t *testing.T) {
		service := newFakeUpdateService()
		started := make(chan struct{})
		release := make(chan struct{})
		service.checkFunc = func(context.Context) (updateapplication.Check, error) {
			close(started)
			<-release
			return updateapplication.Check{Status: "up_to_date"}, nil
		}
		root := testUpdatesRoot(t)
		server := startTestServer(t, root, service, Options{CloseGrace: 10 * time.Millisecond, CloseTimeout: 20 * time.Millisecond})
		client := &localClient{path: SocketPath(root)}
		callDone := make(chan *commandError, 1)
		go func() {
			_, err := client.call("check", "", time.Second)
			callDone <- err
		}()
		<-started
		began := time.Now()
		err := server.Close()
		if err == nil || !strings.Contains(err.Error(), "close deadline") {
			t.Fatalf("Close error = %v, want bounded-handler error", err)
		}
		if elapsed := time.Since(began); elapsed > 500*time.Millisecond {
			t.Fatalf("Close waited %s for an uncooperative handler", elapsed)
		}
		close(release)
		if callErr := <-callDone; callErr == nil {
			t.Fatal("forcibly closed client unexpectedly succeeded")
		}
	})
}

func TestServerRejectsUnauthorizedPeerBeforeServiceCall(t *testing.T) {
	service := newFakeUpdateService()
	checkCalled := make(chan struct{}, 1)
	service.checkFunc = func(context.Context) (updateapplication.Check, error) {
		checkCalled <- struct{}{}
		return updateapplication.Check{Status: "up_to_date"}, nil
	}
	root := testUpdatesRoot(t)
	startTestServer(t, root, service, Options{peerIdentity: func(*net.UnixConn) (uint32, error) {
		return uint32(os.Geteuid() + 1), nil
	}})
	client := &localClient{path: SocketPath(root)}
	_, err := client.call("check", "", time.Second)
	if err == nil || err.exit != exitAuth || err.code != "UPDATE_LOCAL_PERMISSION" {
		t.Fatalf("unauthorized peer error = %v", err)
	}
	select {
	case <-checkCalled:
		t.Fatal("unauthorized peer reached the application service")
	default:
	}
}

func TestWaitReconnectsAfterServiceRestart(t *testing.T) {
	root := testUpdatesRoot(t)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	service := newFakeUpdateService()
	service.statusFunc = func(context.Context) (updateapplication.Status, bool, error) {
		return fakeStatus("succeeded"), true, nil
	}
	type listenResult struct {
		server *Server
		err    error
	}
	started := make(chan listenResult, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		server, err := Listen(root, service, Options{})
		started <- listenResult{server: server, err: err}
	}()
	oldInterval := waitPollInterval
	waitPollInterval = time.Millisecond
	t.Cleanup(func() { waitPollInterval = oldInterval })
	client := &localClient{path: SocketPath(root)}
	final, waitErr := client.wait(updatetransport.Status(fakeStatus("accepted")), 20*time.Millisecond, time.Second)
	listened := <-started
	if listened.err != nil {
		t.Fatalf("restart listener failed: %v", listened.err)
	}
	if err := listened.server.Close(); err != nil {
		t.Fatal(err)
	}
	if waitErr != nil || final.Step != "succeeded" {
		t.Fatalf("wait final %+v error %v", final, waitErr)
	}
}

func TestRequestBoundsAndConnectionDeadline(t *testing.T) {
	valid := []byte("{\"version\":1,\"action\":\"check\"}\n")
	if _, err := readRequest(bytes.NewReader(valid)); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	for name, raw := range map[string][]byte{
		"too large":     append(bytes.Repeat([]byte{' '}, maxRequestBytes+1), '\n'),
		"unknown field": []byte("{\"version\":1,\"action\":\"check\",\"extra\":true}\n"),
		"bad version":   []byte("{\"version\":2,\"action\":\"check\"}\n"),
		"two documents": []byte("{\"version\":1,\"action\":\"check\"} {}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readRequest(bytes.NewReader(raw)); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}

	service := newFakeUpdateService()
	root := testUpdatesRoot(t)
	startTestServer(t, root, service, Options{ReadTimeout: 20 * time.Millisecond})
	connection, err := net.Dial("unix", SocketPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	var result response
	if err := json.NewDecoder(connection).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Code != "UPDATE_LOCAL_REQUEST_INVALID" {
		t.Fatalf("read deadline response = %+v", result.Error)
	}
}

func TestConnectionLimitShedsExcessClients(t *testing.T) {
	service := newFakeUpdateService()
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	service.checkFunc = func(context.Context) (updateapplication.Check, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return updateapplication.Check{Status: "up_to_date"}, nil
	}
	defer releaseOnce.Do(func() { close(release) })
	root := testUpdatesRoot(t)
	startTestServer(t, root, service, Options{MaxConnections: 1})
	client := &localClient{path: SocketPath(root)}
	firstDone := make(chan *commandError, 1)
	go func() {
		_, err := client.call("check", "", time.Second)
		firstDone <- err
	}()
	<-started
	second, err := net.Dial("unix", SocketPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buffer := make([]byte, 1)
	if _, err := second.Read(buffer); err == nil {
		t.Fatal("excess connection received a response instead of being shed")
	} else if !errors.Is(err, io.EOF) {
		t.Fatalf("excess connection error = %v, want EOF", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-firstDone; err != nil {
		t.Fatalf("first request failed: %v", err)
	}
}

func TestSocketSafetyAndCleanup(t *testing.T) {
	t.Run("active listener is never replaced", func(t *testing.T) {
		root := testUpdatesRoot(t)
		startTestServer(t, root, newFakeUpdateService(), Options{})
		second, err := Listen(root, newFakeUpdateService(), Options{})
		if err == nil || second != nil || !strings.Contains(err.Error(), "active listener") {
			t.Fatalf("second listener = %v, %v", second, err)
		}
		if _, err := os.Lstat(SocketPath(root)); err != nil {
			t.Fatalf("active socket removed: %v", err)
		}
	})

	t.Run("non socket is preserved", func(t *testing.T) {
		root := testUpdatesRoot(t)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := SocketPath(root)
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(root, newFakeUpdateService(), Options{}); err == nil {
			t.Fatal("regular file was replaced")
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != "keep" {
			t.Fatalf("regular file changed: %q, %v", raw, err)
		}
	})

	t.Run("stale socket is reclaimed and closed by inode", func(t *testing.T) {
		root := testUpdatesRoot(t)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := SocketPath(root)
		stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		stale.SetUnlinkOnClose(false)
		_ = stale.Close()
		server := startTestServer(t, root, newFakeUpdateService(), Options{})
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != "replacement" {
			t.Fatalf("replacement path was removed: %q, %v", raw, err)
		}
	})

	t.Run("symlink parent refused", func(t *testing.T) {
		parent := t.TempDir()
		real := filepath.Join(parent, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(link, newFakeUpdateService(), Options{}); err == nil {
			t.Fatal("symlink updates root accepted")
		}
	})

	t.Run("writable parent refused", func(t *testing.T) {
		root := testUpdatesRoot(t)
		if err := os.Mkdir(root, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(root, newFakeUpdateService(), Options{}); err == nil {
			t.Fatal("world-writable updates root accepted")
		}
	})
}

func TestErrorMappingDoesNotLeakInternals(t *testing.T) {
	mapped := mapError(errors.New("postgres password and /private/path"))
	if mapped.Code != "INTERNAL" || mapped.Status != 500 || strings.Contains(mapped.Message, "postgres") || strings.Contains(mapped.Message, "/private") {
		t.Fatalf("unsafe internal mapping: %+v", mapped)
	}
	business := mapError(&apperrors.Business{Status: 409, Code: "UPDATE_LOCKED", Message: "An update is already in progress."})
	if business.Code != "UPDATE_LOCKED" || business.Status != 409 {
		t.Fatalf("business mapping: %+v", business)
	}
	if !authorizedUID(0, 1000) || !authorizedUID(1000, 1000) || authorizedUID(1001, 1000) {
		t.Fatal("peer UID authorization table is incorrect")
	}
}

func testUpdatesRoot(t *testing.T) string {
	t.Helper()
	parent, err := os.MkdirTemp("", "bu-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	return filepath.Join(parent, "u")
}

func testEnvironment(root string) func(string) string {
	return func(key string) string {
		switch key {
		case "BANSOKO_UPDATE_MODE":
			return "embedded"
		case "BANSOKO_UPDATES_ROOT":
			return root
		default:
			return ""
		}
	}
}

func assertErrorDocument(t *testing.T, code int, raw string, wantCode int, wantError string) {
	t.Helper()
	if code != wantCode {
		t.Fatalf("exit code %d, want %d; document %q", code, wantCode, raw)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatalf("invalid error JSON %q: %v", raw, err)
	}
	errorValue, ok := document["error"].(map[string]any)
	if !ok || errorValue["code"] != wantError || errorValue["exit_code"] != float64(wantCode) {
		t.Fatalf("error document = %v, want %s/%d", document, wantError, wantCode)
	}
}
