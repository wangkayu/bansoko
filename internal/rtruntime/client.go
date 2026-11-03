package rtruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	reltitlemodel "github.com/wangkayu/reltitle/pkg/model"
)

const (
	// requestTimeout bounds one protocol request. The parser answers a title in
	// microseconds; a reply this late means the process is wedged, and the
	// client kills it rather than waiting for the process deadline.
	requestTimeout = 10 * time.Second

	// versionTimeout bounds the `-version` self-check.
	versionTimeout = 5 * time.Second

	// maxResponseBytes bounds one response line. It mirrors the protocol's own
	// bound so a hostile binary cannot exhaust memory with one line.
	maxResponseBytes = 1 << 20

	// maxStderrBytes keeps the tail of a crashed process for the note.
	maxStderrBytes = 4 << 10
)

// ErrTransport reports a broken process rather than a refused title: the
// binary died, closed its pipes or answered something that is not the
// protocol. The caller may restart once and retry.
var ErrTransport = errors.New("reltitle process transport failed")

// errResponseTooLong reports an answer line that passed the response cap while
// it was still being read.
var errResponseTooLong = errors.New("response line exceeds the cap")

// Client is one running `reltitle serve` process. Calls are serialized: one
// title at a time is the protocol's shape, and parsing is not this program's
// hot path. The client owns the process and reaps it on Close.
type Client struct {
	path string
	info Info

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	reader   *bufio.Reader
	nextID   int64
	shutdown bool

	stderrTail *tailBuffer
}

// Start spawns the binary and completes the hello handshake. The caller owns
// the returned client and must Close it.
func Start(ctx context.Context, path string) (*Client, error) {
	client := &Client{path: path}
	if err := client.start(); err != nil {
		return nil, err
	}
	hello, err := client.hello(ctx)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if hello.Protocol != Protocol {
		_ = client.Close()
		return nil, fmt.Errorf("%s speaks %q; this build speaks %q", path, hello.Protocol, Protocol)
	}
	client.info = Info{Source: SourceManual, Version: hello.Version, Line: hello.Line, ConfigDigest: hello.ConfigDigest, Path: path}
	return client, nil
}

func (c *Client) start() error {
	cmd := exec.Command(c.path, "serve")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTransport, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTransport, err)
	}
	tail := &tailBuffer{limit: maxStderrBytes}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %v", ErrTransport, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.reader = bufio.NewReaderSize(stdout, 64<<10)
	c.stderrTail = tail
	// Reap the process when it exits; Wait must run exactly once per start.
	go func(cmd *exec.Cmd) { _ = cmd.Wait() }(cmd)
	return nil
}

// Info describes the running binary.
func (c *Client) Info() Info { return c.info }

// Close stops the process. It is safe to call twice.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stop()
}

func (c *Client) stop() error {
	c.shutdown = true
	return c.kill()
}

// kill stops the process without shutting the client down, so a later call can
// restart it.
func (c *Client) kill() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		c.cmd = nil
	}
	return nil
}

// Parse runs one title. A transport failure restarts the process once and
// retries; a second failure is returned, and the caller falls back to the
// builtin parser.
func (c *Client) Parse(ctx context.Context, title string) (*reltitlemodel.ParseResult, error) {
	raw, err := c.callWithRestart(ctx, "parse", title)
	if err != nil {
		return nil, err
	}
	var result reltitlemodel.ParseResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%w: parse result is not the reltitle model: %v", ErrTransport, err)
	}
	return &result, nil
}

// Vocabulary returns the binary's attribute dimensions and canonical values.
// bansoko projects release-title tags through this dictionary, so a custom
// build is projected through its own values rather than the pinned ones.
func (c *Client) Vocabulary(ctx context.Context) ([]Dimension, error) {
	raw, err := c.callWithRestart(ctx, "vocabulary", "")
	if err != nil {
		return nil, err
	}
	var result vocabularyResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%w: vocabulary is not the protocol shape: %v", ErrTransport, err)
	}
	return result.Dimensions, nil
}

// Dimension is one attribute dimension with its canonical values.
type Dimension struct {
	Dimension string   `json:"dimension"`
	Values    []string `json:"values"`
}

type vocabularyResult struct {
	Dimensions []Dimension `json:"dimensions"`
}

type helloResult struct {
	Protocol     string `json:"protocol"`
	Version      string `json:"version"`
	Line         string `json:"line"`
	ConfigDigest string `json:"config_digest"`
}

type response struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) hello(ctx context.Context) (helloResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.helloLocked(ctx)
}

// helloLocked runs the handshake with c.mu already held, so a restart can
// re-prove the protocol before the retried call is written.
func (c *Client) helloLocked(ctx context.Context) (helloResult, error) {
	raw, err := c.callLocked(ctx, "hello", "")
	if err != nil {
		return helloResult{}, err
	}
	var hello helloResult
	if err := json.Unmarshal(raw, &hello); err != nil {
		return helloResult{}, fmt.Errorf("%w: hello is not the protocol shape: %v", ErrTransport, err)
	}
	return hello, nil
}

// smoke proves the binary can parse before it is adopted.
func (c *Client) smoke(ctx context.Context) error {
	_, err := c.Parse(ctx, smokeTitle)
	return err
}

// callWithRestart retries one transport failure after restarting the process.
// A protocol-level refusal (ok:false) is never retried: the binary answered.
func (c *Client) callWithRestart(ctx context.Context, op, title string) (json.RawMessage, error) {
	raw, err := c.call(ctx, op, title)
	if !errors.Is(err, ErrTransport) {
		return raw, err
	}
	c.mu.Lock()
	restartErr := c.restartLocked()
	c.mu.Unlock()
	if restartErr != nil {
		return nil, fmt.Errorf("%w: restart after %v failed: %v", ErrTransport, err, restartErr)
	}
	return c.call(ctx, op, title)
}

func (c *Client) restartLocked() error {
	if c.shutdown {
		return errors.New("client is closed")
	}
	_ = c.kill()
	if err := c.start(); err != nil {
		return err
	}
	// The file at c.path may have been replaced since the first start. The
	// handshake re-proves the protocol of whatever binary just spawned, so a
	// silent protocol change cannot reach the retried call.
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	hello, err := c.helloLocked(ctx)
	if err != nil {
		return err
	}
	if hello.Protocol != Protocol {
		return fmt.Errorf("restarted binary speaks %q; this build speaks %q", hello.Protocol, Protocol)
	}
	return nil
}

// call writes one request and reads its answer. The request id ties the answer
// to the request when a restarted process left unread lines behind.
func (c *Client) call(ctx context.Context, op, title string) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callLocked(ctx, op, title)
}

func (c *Client) callLocked(ctx context.Context, op, title string) (json.RawMessage, error) {
	if c.shutdown {
		return nil, fmt.Errorf("%w: the client is closed", ErrTransport)
	}
	if c.stdin == nil || c.cmd == nil {
		return nil, fmt.Errorf("%w: the process is gone", ErrTransport)
	}
	c.nextID++
	id := c.nextID
	request := map[string]any{"id": id, "op": op}
	if title != "" {
		request["title"] = title
	}
	line, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(requestTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		_ = c.kill()
		return nil, fmt.Errorf("%w: write: %v", ErrTransport, err)
	}
	process := c.cmd.Process
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
		case <-time.After(time.Until(deadline)):
			if process != nil {
				_ = process.Kill()
			}
		}
	}()
	raw, err := readLineCapped(c.reader, maxResponseBytes)
	if err != nil {
		_ = c.kill()
		if errors.Is(err, errResponseTooLong) {
			return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrTransport, maxResponseBytes)
		}
		if tail := c.stderrTail.String(); tail != "" {
			return nil, fmt.Errorf("%w: read: %v (%s)", ErrTransport, err, strings.TrimSpace(tail))
		}
		return nil, fmt.Errorf("%w: read: %v", ErrTransport, err)
	}
	var answer response
	if err := json.Unmarshal(bytes.TrimSpace(raw), &answer); err != nil {
		return nil, fmt.Errorf("%w: response is not JSON: %v", ErrTransport, err)
	}
	if answer.ID != id {
		return nil, fmt.Errorf("%w: response id %d does not match request %d", ErrTransport, answer.ID, id)
	}
	if !answer.OK {
		if answer.Error == nil {
			return nil, fmt.Errorf("%w: refused without an error", ErrTransport)
		}
		if answer.Error.Code == "parse_failed" {
			// An unparseable title is the binary's verdict, not a transport
			// failure: the caller reports it as a parse failure.
			return nil, fmt.Errorf("%s: %s", answer.Error.Code, answer.Error.Message)
		}
		return nil, fmt.Errorf("%w: %s: %s", ErrTransport, answer.Error.Code, answer.Error.Message)
	}
	return answer.Result, nil
}

// readLineCapped reads one newline-terminated answer while enforcing the cap
// on the way in: a binary that streams an endless line is cut at the cap, not
// buffered whole and rejected afterwards.
func readLineCapped(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		frag, err := reader.ReadSlice('\n')
		if len(line)+len(frag) > limit {
			return nil, errResponseTooLong
		}
		line = append(line, frag...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// tailBuffer keeps the last n bytes written, for a crash note.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// VersionOf runs `-version` on a candidate binary and returns its reported
// version. It is the first executable step of the adoption chain: a file that
// cannot run at all is not this program.
func VersionOf(ctx context.Context, path string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	output, err := exec.CommandContext(runCtx, path, "-version").Output()
	if err != nil {
		return "", fmt.Errorf("%s -version failed: %v", path, err)
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", fmt.Errorf("%s -version printed nothing", path)
	}
	return version, nil
}
