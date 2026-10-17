// Package run executes external commands with a deadline.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

var ErrTimeout = errors.New("timed out")

// Error is a command that exited non-zero. Msg carries the command line, stderr and a stdout excerpt.
// Exit is the exit code, or -1 when the process was killed or never started.
type Error struct {
	Msg, Stderr string
	Exit        int
	Err         error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// Cmd runs name in dir and returns stdout. Errors carry the command line, stderr and a stdout excerpt.
// After the timeout it waits at most 5s more for children still holding the output pipes.
// Every run is logged: DEBUG on success, WARN with stderr and stdout excerpts on failure.
func Cmd(ctx context.Context, timeout time.Duration, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return CmdEnv(ctx, timeout, dir, nil, stdin, name, args...)
}

// CmdEnv is Cmd with extra appended to the child's environment.
func CmdEnv(ctx context.Context, timeout time.Duration, dir string, extra []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env, c.Stdin = dir, append(Env(), extra...), stdin
	return output(ctx, c, " after "+timeout.String())
}

// Group runs name in dir with exactly env and no deadline of its own, in a new process group. Ending ctx
// sends SIGTERM to the group, so the command can end its own children; whatever is left in the group when
// the command returns gets SIGKILL. Output, errors and logging are as for Cmd.
func Group(ctx context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env, c.Stdin = dir, env, stdin
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
	out, err := output(ctx, c, "")
	if c.Process != nil {
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	return out, err
}

// output runs c and returns stdout. A deadline that ends ctx is ErrTimeout; a non-zero exit is *Error.
// After ctx ends it waits at most 5s more for children still holding the output pipes.
func output(ctx context.Context, c *exec.Cmd, after string) ([]byte, error) {
	c.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	c.Stderr = &stderr
	start := time.Now()
	out, err := c.Output()
	exit := -1
	if c.ProcessState != nil {
		exit = c.ProcessState.ExitCode()
	}
	line := strings.Join(c.Args, " ")
	attrs := []any{"argv", c.Args, "dir", c.Dir, "duration_ms", time.Since(start).Milliseconds(), "exit", exit}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.WarnContext(ctx, "exec timed out", append(attrs, "stderr", tail(stderr.String()))...)
		return out, fmt.Errorf("%s: %w%s", line, ErrTimeout, after)
	}
	if err != nil {
		slog.WarnContext(ctx, "exec failed", append(attrs, "stderr", tail(stderr.String()), "stdout", tail(string(out)))...)
		e := &Error{Stderr: strings.TrimSpace(stderr.String()), Exit: exit, Err: err}
		e.Msg = fmt.Sprintf("%s: %v: %s", line, err, e.Stderr)
		if s := tail(string(out)); s != "" {
			e.Msg += "\nstdout: " + s
		}
		return out, e
	}
	slog.DebugContext(ctx, "exec", attrs...)
	return out, nil
}

// repoLocal is what `git rev-parse --local-env-vars` prints: the variables that pin git to one repository.
var repoLocal = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
	"GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX", "GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
}

// Env is this process's environment without git's repo-local variables, so a child acts on the
// directory it runs in even when foundry runs from inside a git hook.
func Env() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(repoLocal, name)
	})
}

// tail keeps the first and last 2 KB of s: tools print their reason first and their stack last.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 4096 {
		return s[:2048] + "\n...\n" + s[len(s)-2048:]
	}
	return s
}
