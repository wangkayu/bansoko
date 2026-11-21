package eventactions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func canExecuteFile(_, fd int, _ string) bool {
	return unix.Faccessat(fd, "", unix.X_OK, unix.AT_EACCESS|unix.AT_EMPTY_PATH) == nil
}

func canRun() bool { return os.Geteuid() != 0 }

func execute(ctx context.Context, file *os.File, stdin []byte, timeout time.Duration, capture *resultCapture) (string, *int, *time.Time) {
	// The inherited descriptor pins the inode inspected above even if the
	// host renames or replaces the directory entry. The mount is not a sandbox.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/proc/self/fd/3")
	cmd.ExtraFiles = []*os.File{file}
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=" + executionPath, "LANG=C.UTF-8", "TZ=UTC"}
	cmd.Stdin = bytes.NewReader(stdin)
	var reader, writer *os.File
	if capture != nil {
		if r, w, err := os.Pipe(); err == nil {
			reader, writer = r, w
			defer reader.Close()
			defer writer.Close()
			cmd.Stdout = writer
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return "failed", nil, nil
	}
	startedAt := time.Now().UTC()
	var finished chan error
	if reader != nil {
		_ = writer.Close()
		finished = make(chan error, 1)
		go func() {
			_, err := io.Copy(capture, reader)
			finished <- err
		}()
	}
	err := cmd.Wait()
	interrupted := ctx.Err() != nil
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	if finished != nil {
		// A descendant holding stdout cannot block settlement or count as a full report.
		if err := reader.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
			_ = reader.Close()
		}
		if err := <-finished; err != nil {
			capture.overflow = true
		}
	}
	if interrupted {
		return "unconfirmed", nil, &startedAt
	}
	if timedOut {
		return "timeout", nil, &startedAt
	}
	code := cmd.ProcessState.ExitCode()
	if err != nil {
		return "failed", &code, &startedAt
	}
	return "completed", &code, &startedAt
}
