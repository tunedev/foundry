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
	"time"
)

var ErrTimeout = errors.New("timed out")

// Error is a command that exited non-zero. Msg carries the command line, stderr and a stdout excerpt.
type Error struct {
	Msg, Stderr string
	Err         error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// Cmd runs name in dir and returns stdout. Errors carry the command line, stderr and a stdout excerpt.
// After the timeout it waits at most 5s more for children still holding the output pipes.
// Every run is logged: DEBUG on success, WARN with stderr and stdout excerpts on failure.
func Cmd(ctx context.Context, timeout time.Duration, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env = dir, Env()
	c.WaitDelay = 5 * time.Second
	c.Stdin = stdin
	var stderr bytes.Buffer
	c.Stderr = &stderr
	start := time.Now()
	out, err := c.Output()
	exit := -1
	if c.ProcessState != nil {
		exit = c.ProcessState.ExitCode()
	}
	attrs := []any{"argv", append([]string{name}, args...), "dir", dir, "duration_ms", time.Since(start).Milliseconds(), "exit", exit}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.WarnContext(ctx, "exec timed out", append(attrs, "stderr", tail(stderr.String()))...)
		return out, fmt.Errorf("%s %s: %w after %s", name, strings.Join(args, " "), ErrTimeout, timeout)
	}
	if err != nil {
		slog.WarnContext(ctx, "exec failed", append(attrs, "stderr", tail(stderr.String()), "stdout", tail(string(out)))...)
		e := &Error{Stderr: strings.TrimSpace(stderr.String()), Err: err}
		e.Msg = fmt.Sprintf("%s %s: %v: %s", name, strings.Join(args, " "), err, e.Stderr)
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
