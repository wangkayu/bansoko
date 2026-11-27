package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/readiness"
	embeddedupdate "bansoko/internal/update/journal"
)

const (
	PrivateReadinessCommand = "--internal-readiness-probe"
	privateProbeTokenEnv    = "BANSOKO_INTERNAL_PROBE_TOKEN"
	privateProbeDeadline    = 30 * time.Second
)

type privateProbeTarget struct {
	binary, schema, frontend string
	env                      map[string]string
	build                    buildinfo.BuildInfo
}

// The only listener in the child is inherited from this supervisor. The
// public maintenance listener is never handed to the candidate.
func runPrivateProbe(root string, target privateProbeTarget) error {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return errors.New("cannot generate private readiness credential")
	}
	dir, err := os.MkdirTemp("", "bsk-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ready.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	file, err := listener.File()
	if err != nil {
		return err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), privateProbeDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, target.binary, PrivateReadinessCommand)
	cmd.ExtraFiles = []*os.File{file}
	env := target.env
	env[privateProbeTokenEnv] = hex.EncodeToString(secret[:])
	env[deploymentidentity.EnvUpdatesRoot] = root
	env[envSchemaDir] = target.schema
	env[envFrontendDir] = target.frontend
	cmd.Env = envWithOverrides(env)
	if err := cmd.Start(); err != nil {
		return errors.New("cannot start private readiness child")
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() { cancel(); <-done }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: probeHTTPTimeout}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return errors.New("private readiness child exited without proof")
		case <-ctx.Done():
			return errors.New("private readiness deadline exceeded")
		case <-tick.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://probe"+readyzPath, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+env[privateProbeTokenEnv])
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			var report readiness.Report
			err = json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&report)
			_ = resp.Body.Close()
			if err == nil && resp.StatusCode == http.StatusOK && report.Ready && matchesProbeIdentity(report.DeploymentIdentity, target) {
				return nil
			}
			if err == nil && resp.StatusCode == http.StatusServiceUnavailable {
				return errors.New("private readiness rejected candidate: " + report.Reason)
			}
		}
	}
}

func matchesProbeIdentity(got *deploymentidentity.Identity, target privateProbeTarget) bool {
	want := deploymentidentity.FromEnv(func(key string) string { return target.env[key] })
	return got != nil && got.SelectedBuild != nil && got.SelectedBuild.Order == target.build.Order &&
		got.SelectedBuild.Commit == target.build.Commit && got.SelectedBuild.BuildID == target.build.BuildID &&
		got.SelectedBuild.Version == target.build.Version && got.SelectedBuild.SchemaVersion == target.build.SchemaVersion &&
		got.SelectedBuild.APIVersion == target.build.APIVersion &&
		(want.SelectedSource != deploymentidentity.SourceBase || *got.SelectedBuild == target.build) &&
		got.ProcessBuildID == target.build.BuildID && got.SelectedSource == want.SelectedSource &&
		got.BaseImageDigest == want.BaseImageDigest && got.SelectedReleaseID == want.SelectedReleaseID &&
		got.PayloadDigest == want.PayloadDigest && got.UpdateOperationID == want.UpdateOperationID
}

// RunPrivateReadiness serves only readiness on a private inherited Unix
// listener. The caller composes read-only startup evidence instead of the
// product router, worker runtime or update service.
func RunPrivateReadiness(report func(context.Context) readiness.Report) int {
	token := os.Getenv(privateProbeTokenEnv)
	if raw, err := hex.DecodeString(token); err != nil || len(raw) != 32 {
		return 2
	}
	file := os.NewFile(3, "private-readiness-listener")
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		return 2
	}
	defer listener.Close()
	if addr, ok := listener.Addr().(*net.UnixAddr); !ok || !strings.HasSuffix(addr.Name, "/ready.sock") {
		return 2
	}
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != readyzPath ||
			subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.NotFound(w, req)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		result := report(ctx)
		status := http.StatusServiceUnavailable
		if result.Ready {
			status = http.StatusOK
		}
		writeMaintenanceJSON(w, status, result)
	})}
	timer := time.AfterFunc(privateProbeDeadline, func() { _ = server.Close() })
	defer timer.Stop()
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

// VerifyPrivateBasePointer grants the old-pointer exception only to the
// journal's exact candidate and only before promotion.
func VerifyPrivateBasePointer(root string, build buildinfo.BuildInfo, pointer deploymentidentity.PointerState) error {
	if !pointer.Presented || os.Getenv(deploymentidentity.EnvSource) != deploymentidentity.SourceBase {
		return nil
	}
	r, found, err := embeddedupdate.Snapshot(filepath.Join(root, journalFileName))
	if err != nil || !found || r.Takeover == nil || r.Step != embeddedupdate.StepMigrationSucceeded ||
		!pointer.Readable || pointer.Corrupt || pointer.Token != r.PreviousReleaseID ||
		r.Takeover.Anchor.Build != build || r.Takeover.Anchor.ImageDigest != os.Getenv(deploymentidentity.EnvBaseImageDigest) {
		return errors.New("private base probe cannot prove the previous pointer against the active takeover")
	}
	return nil
}
