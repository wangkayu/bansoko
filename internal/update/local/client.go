package localupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/transport/httpapi/generated"
)

const (
	exitOK          = 0
	exitOther       = 1
	exitUsage       = 2
	exitAuth        = 3
	exitNotFound    = 4
	exitConflict    = 5
	exitUnavailable = 6
	exitWaitTimeout = 7

	defaultRequestTimeout = 10 * time.Minute
	defaultWaitTimeout    = 30 * time.Minute
	maxResponseBytes      = 16 << 20
)

var waitPollInterval = 2 * time.Second

// Main is the update subcommand entry in the unified server binary.
func Main(args []string) int {
	return Run(args, os.Getenv, os.Stdout, os.Stderr)
}

// Run executes one local command. JSON mode writes exactly one result or error
// document to stdout; explanatory human output then goes to stderr.
func Run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	args, jsonMode := extractJSON(args)
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		_, _ = fmt.Fprint(stdout, usage())
		return exitOK
	}
	options, help, err := parseOptions(args)
	if help {
		_, _ = fmt.Fprint(stdout, usage())
		return exitOK
	}
	if err != nil {
		return writeError(jsonMode, stdout, stderr, &commandError{exit: exitUsage, code: "USAGE", message: err.Error()})
	}
	if !Supported {
		return writeError(jsonMode, stdout, stderr, &commandError{exit: exitUnavailable, code: "UPDATE_LOCAL_UNSUPPORTED", message: "Local update control requires kernel peer credentials on Linux or Darwin; this platform is unsupported."})
	}
	if config.UpdateModeFromEnv(getenv) != "embedded" {
		return writeError(jsonMode, stdout, stderr, &commandError{exit: exitUnavailable, code: "SERVICE_NOT_CONFIGURED", message: "Local update control is available only when BANSOKO_UPDATE_MODE=embedded."})
	}
	client := &localClient{path: SocketPath(deploymentidentity.UpdatesRoot(getenv))}
	switch options.action {
	case "check":
		result, callErr := client.call(options.action, "", options.requestTimeout)
		if callErr != nil {
			return writeError(jsonMode, stdout, stderr, callErr)
		}
		if result.Check == nil {
			return writeError(jsonMode, stdout, stderr, invalidResponse())
		}
		writeResult(jsonMode, stdout, stderr, result.Check, func(writer io.Writer) { printCheck(writer, result.Check) })
		return exitOK
	case "status":
		result, callErr := client.call(options.action, "", options.requestTimeout)
		if callErr != nil {
			return writeError(jsonMode, stdout, stderr, callErr)
		}
		if result.Status == nil {
			return writeError(jsonMode, stdout, stderr, invalidResponse())
		}
		writeResult(jsonMode, stdout, stderr, result.Status, func(writer io.Writer) { printStatus(writer, result.Status) })
		return exitOK
	case "apply":
		key, keyErr := newIdempotencyKey()
		if keyErr != nil {
			return writeError(jsonMode, stdout, stderr, &commandError{exit: exitOther, code: "UPDATE_LOCAL_KEY_FAILED", message: "A local request identity could not be generated."})
		}
		result, callErr := client.call(options.action, key, options.requestTimeout)
		if callErr != nil {
			if callErr.code == "UPDATE_LOCAL_TIMEOUT" {
				callErr.message = "The apply request timed out. Admission is unknown, not failed; run update status before retrying."
			}
			return writeError(jsonMode, stdout, stderr, callErr)
		}
		if result.Status == nil || result.Status.OperationId == "" {
			return writeError(jsonMode, stdout, stderr, invalidResponse())
		}
		accepted := result.Status
		if !options.wait {
			writeResult(jsonMode, stdout, stderr, accepted, func(writer io.Writer) { printAccepted(writer, accepted) })
			return exitOK
		}
		if jsonMode {
			printAccepted(stderr, accepted)
		} else {
			printAccepted(stdout, accepted)
		}
		final, waitErr := client.wait(*accepted, options.requestTimeout, options.waitTimeout)
		if waitErr != nil {
			return writeError(jsonMode, stdout, stderr, waitErr)
		}
		writeResult(jsonMode, stdout, stderr, final, func(writer io.Writer) { printStatus(writer, final) })
		if terminalFailure(final.Step) {
			_, _ = fmt.Fprintf(stderr, "Update reached terminal step %s; this is not a successful update. 更新已到达终态 %s，并非成功。\n", final.Step, final.Step)
			return exitOther
		}
		return exitOK
	default:
		return writeError(jsonMode, stdout, stderr, &commandError{exit: exitUsage, code: "USAGE", message: "expected update check, apply, or status"})
	}
}

type commandOptions struct {
	action         string
	requestTimeout time.Duration
	wait           bool
	waitTimeout    time.Duration
}

func parseOptions(args []string) (commandOptions, bool, error) {
	if len(args) == 0 {
		return commandOptions{}, false, errors.New("expected update check, apply, or status; use update help for usage")
	}
	options := commandOptions{action: args[0], requestTimeout: defaultRequestTimeout, waitTimeout: defaultWaitTimeout}
	switch options.action {
	case "check", "apply", "status":
	default:
		return commandOptions{}, false, fmt.Errorf("unsupported update action %q", options.action)
	}
	flags := flag.NewFlagSet("update "+options.action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.DurationVar(&options.requestTimeout, "timeout", defaultRequestTimeout, "local request timeout")
	flags.BoolVar(&options.wait, "wait", false, "wait for a terminal update outcome")
	flags.DurationVar(&options.waitTimeout, "wait-timeout", defaultWaitTimeout, "overall wait timeout")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return commandOptions{}, true, nil
		}
		return commandOptions{}, false, err
	}
	if flags.NArg() != 0 {
		return commandOptions{}, false, errors.New("unexpected positional arguments")
	}
	if options.requestTimeout <= 0 || options.waitTimeout <= 0 {
		return commandOptions{}, false, errors.New("timeouts must be positive durations")
	}
	if options.action != "apply" && (options.wait || options.waitTimeout != defaultWaitTimeout) {
		return commandOptions{}, false, errors.New("--wait and --wait-timeout are valid only for update apply")
	}
	if !options.wait && options.waitTimeout != defaultWaitTimeout {
		return commandOptions{}, false, errors.New("--wait-timeout requires --wait")
	}
	return options, false, nil
}

type commandError struct {
	exit    int
	code    string
	message string
}

func (e *commandError) Error() string { return e.message }

func invalidResponse() *commandError {
	return &commandError{exit: exitUnavailable, code: "UPDATE_LOCAL_PROTOCOL", message: "The running service returned an invalid local update response."}
}

type localClient struct {
	path string
}

func (c *localClient) call(action, key string, timeout time.Duration) (response, *commandError) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", c.path)
	if err != nil {
		return response{}, localTransportError(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	req := request{Version: protocolVersion, Action: action, IdempotencyKey: key}
	if err := json.NewEncoder(connection).Encode(req); err != nil {
		return response{}, localTransportError(err)
	}
	var result response
	decoder := json.NewDecoder(io.LimitReader(connection, maxResponseBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return response{}, localTransportError(err)
	}
	if result.Error != nil {
		return result, &commandError{exit: exitForStatus(result.Error.Status), code: result.Error.Code, message: result.Error.Message}
	}
	return result, nil
}

func localTransportError(err error) *commandError {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return &commandError{exit: exitUnavailable, code: "UPDATE_LOCAL_TIMEOUT", message: "The local update request timed out."}
	}
	if errors.Is(err, os.ErrPermission) {
		return &commandError{exit: exitAuth, code: "UPDATE_LOCAL_PERMISSION", message: "Local update control requires root or the serving PUID."}
	}
	return &commandError{exit: exitUnavailable, code: "UPDATE_LOCAL_UNAVAILABLE", message: "The running update service is unavailable while starting, draining, in maintenance, or on an older release. Retry after restart; apply --wait retries automatically."}
}

func exitForStatus(status int) int {
	switch {
	case status == 401 || status == 403:
		return exitAuth
	case status == 404 || status == 410:
		return exitNotFound
	case status == 409 || status == 412 || status == 422 || status == 428:
		return exitConflict
	case status == 400:
		return exitUsage
	case status >= 500:
		return exitUnavailable
	default:
		return exitOther
	}
}

func (c *localClient) wait(accepted generated.EmbeddedUpdateStatus, requestTimeout, waitTimeout time.Duration) (*generated.EmbeddedUpdateStatus, *commandError) {
	deadline := time.Now().Add(waitTimeout)
	current := accepted
	for {
		if terminalStep(current.Step) {
			return &current, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, &commandError{exit: exitWaitTimeout, code: "UPDATE_WAIT_TIMEOUT", message: "Waiting for the update timed out. The outcome is unknown, not failed; run update status after the service restarts."}
		}
		pause := waitPollInterval
		if pause > remaining {
			pause = remaining
		}
		time.Sleep(pause)
		remaining = time.Until(deadline)
		if remaining <= 0 {
			continue
		}
		callTimeout := requestTimeout
		if callTimeout > remaining {
			callTimeout = remaining
		}
		result, err := c.call("status", "", callTimeout)
		if err != nil {
			if err.code == "UPDATE_STATUS_NOT_FOUND" || err.code == "UPDATE_LOCAL_UNAVAILABLE" || err.code == "UPDATE_LOCAL_TIMEOUT" {
				continue
			}
			return nil, err
		}
		if result.Status == nil {
			return nil, invalidResponse()
		}
		if result.Status.OperationId != accepted.OperationId {
			return nil, &commandError{exit: exitConflict, code: "UPDATE_OPERATION_CHANGED", message: "The running service reports a different update operation; refusing to infer an outcome."}
		}
		current = *result.Status
	}
}

func terminalStep(step string) bool {
	return step == "succeeded" || terminalFailure(step)
}

func terminalFailure(step string) bool {
	switch step {
	case "failed", "rolled_back", "rollback_blocked":
		return true
	default:
		return false
	}
}

func newIdempotencyKey() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "local-update-" + hex.EncodeToString(raw), nil
}

func extractJSON(args []string) ([]string, bool) {
	kept := make([]string, 0, len(args))
	jsonMode := false
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case "--json", "-json", "--json=true", "-json=true":
			jsonMode = true
		case "--json=false", "-json=false":
		default:
			kept = append(kept, arg)
		}
	}
	return kept, jsonMode
}

func writeResult(jsonMode bool, stdout, stderr io.Writer, value any, human func(io.Writer)) {
	if jsonMode {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(value)
		return
	}
	human(stdout)
}

func writeError(jsonMode bool, stdout, stderr io.Writer, err *commandError) int {
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"error": map[string]any{
			"code": err.code, "message": err.message, "exit_code": err.exit,
		}})
	} else {
		_, _ = fmt.Fprintf(stderr, "bansoko-server update: %s: %s\n", err.code, err.message)
	}
	return err.exit
}

func printCheck(writer io.Writer, result *generated.UpdateCheck) {
	_, _ = fmt.Fprintf(writer, "current: %s (%s)\n", result.Current.Version, result.Current.ReleaseId)
	_, _ = fmt.Fprintf(writer, "status: %s\n", result.Status)
	if result.Candidate != nil {
		_, _ = fmt.Fprintf(writer, "candidate: %s (%s)\n", result.Candidate.Version, result.Candidate.ReleaseId)
	}
	_, _ = fmt.Fprintf(writer, "compatibility: %s\n", result.Compatibility.Reason)
	if result.ActiveOperation != nil {
		_, _ = fmt.Fprintf(writer, "active operation: %s step=%s\n", result.ActiveOperation.OperationId, result.ActiveOperation.Step)
	}
}

func printAccepted(writer io.Writer, status *generated.EmbeddedUpdateStatus) {
	_, _ = fmt.Fprintf(writer, "accepted operation: %s step=%s\n", status.OperationId, status.Step)
	_, _ = fmt.Fprintln(writer, "Update accepted, not completed. Use update status, or update apply --wait next time. 更新仅已受理，尚未完成；请查询 status 或使用 --wait。")
}

func printStatus(writer io.Writer, status *generated.EmbeddedUpdateStatus) {
	_, _ = fmt.Fprintf(writer, "operation: %s\nstep: %s\n", status.OperationId, status.Step)
	if status.TargetVersion != nil {
		_, _ = fmt.Fprintf(writer, "target: %s (%s)\n", *status.TargetVersion, status.TargetReleaseId)
	} else {
		_, _ = fmt.Fprintf(writer, "target: %s\n", status.TargetReleaseId)
	}
	if !status.UpdatedAt.IsZero() {
		_, _ = fmt.Fprintf(writer, "updated: %s\n", status.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if status.LastError != nil {
		_, _ = fmt.Fprintf(writer, "last error: %s\n", *status.LastError)
	}
	if terminalStep(status.Step) {
		_, _ = fmt.Fprintf(writer, "terminal outcome: %s (status query succeeded) 终态：%s（本次状态查询成功）\n", status.Step, status.Step)
	}
}

func usage() string {
	return `Container-local embedded update control / 容器本地更新命令

Usage with the current binary / 当前版本命令:
  bansoko-server update check [--json] [--timeout 10m]
  bansoko-server update apply [--json] [--timeout 10m]
  bansoko-server update apply --wait [--wait-timeout 30m] [--json]
  bansoko-server update status [--json] [--timeout 10m]

Container invocation / 容器调用（将最后的 check 换为 apply 或 status）:
  docker exec <container> sh -c '
    root=${BANSOKO_UPDATES_ROOT:-${BANSOKO_CONFIG_DIR:-/config}/updates}
    binary=/app/bansoko-server
    if [ -f "$root/current" ]; then
      release=$(cat "$root/current")
      case "$release" in ""|*[!A-Za-z0-9._-]*|.|..) exit 2;; esac
      binary="$root/releases/$release/bansoko-server"
    fi
    exec "$binary" update "$@"
  ' sh check --json

This reads the existing current pointer; it does not change update state.
/app can remain an older base after a Web UI overlay upgrade. Do not assume
/proc/1/exe is accessible after privilege dropping. Native runs use their
current binary. 此命令只读取既有 current 指针选择 overlay，无 overlay 时使用
/app；不写更新状态，不依赖 /proc 权限。Native 部署使用当前版本 binary。
The caller and the running service must both support local update control.
Older images/overlays do not grow this command; use the existing Web UI or an
image switch for the first supported upgrade. 首次从旧版升级仍用 WebUI 或切换镜像。

Run as root (docker exec default) or the serving PUID. No web session or API
token is read. 仅 root 或服务 PUID 可用，不读取 Web/API 凭据。
apply returns immediately after admission; accepted is not update success.
apply 默认仅受理，受理不代表更新成功。--wait retries while this client is
alive, but Docker may kill docker exec during handoff. A Docker disconnect or
exit code is not an update outcome. After restart, run the container command
above in a new exec, replacing check with status.
--wait 仅在客户端存活时等待重连；handoff 可能使 docker exec 被终止，断连或
Docker 返回码均不代表更新成败；重启后须另起 exec，将上述命令末尾 check 改为 status。

Exit codes / 退出码:
  0 check/status succeeded or apply admitted; 1 update terminal failure with --wait
  2 usage; 3 local permission; 4 no recorded status; 5 conflict/precondition
  6 service unavailable/request failed; 7 explicit wait timeout (outcome unknown)
status itself returns 0 whenever a terminal state was read, including failed.
status 只要成功读取状态即为 0，包括 failed/rolled_back。
`
}
