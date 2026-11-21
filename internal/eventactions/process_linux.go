package eventactions

import (
	"bytes"
	"context"
	"errors"
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

func execute(ctx context.Context, file *os.File, stdin []byte, timeout time.Duration) (string, *int) {
	// The inherited descriptor pins the inode inspected above even if the
	// host renames or replaces the directory entry. The mount is not a sandbox.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/proc/self/fd/3")
	cmd.ExtraFiles = []*os.File{file}
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=" + executionPath, "LANG=C.UTF-8", "TZ=UTC"}
	cmd.Stdin = bytes.NewReader(stdin)
	// Do not persist or expose arbitrary script output (it may contain secrets).
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
		return "failed", nil
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		return "unconfirmed", nil
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return "timeout", nil
	}
	code := cmd.ProcessState.ExitCode()
	if err != nil {
		return "failed", &code
	}
	return "completed", &code
}
