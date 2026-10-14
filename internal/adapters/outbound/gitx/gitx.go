// Package gitx runs git in a checkout.
package gitx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/run"
)

type Git struct{ Timeout time.Duration }

func (g Git) out(ctx context.Context, dir string, args ...string) (string, error) {
	b, err := run.Cmd(ctx, g.Timeout, dir, nil, "git", args...)
	return strings.TrimSpace(string(b)), run.Classify(err)
}

func (g Git) Toplevel(ctx context.Context, dir string) (string, error) {
	return g.out(ctx, dir, "rev-parse", "--show-toplevel")
}

// RepoID returns owner/name from the origin URL, for https, ssh and local-path remotes.
func (g Git) RepoID(ctx context.Context, dir string) (string, error) {
	u, err := g.out(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	s := strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	parts := strings.Split(strings.ReplaceAll(s, ":", "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", fmt.Errorf("origin %q does not name an owner and repo", u)
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

func (g Git) Branch(ctx context.Context, dir string) (string, error) {
	return g.out(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// Clean reports whether the checkout has no staged, unstaged or untracked changes.
func (g Git) Clean(ctx context.Context, dir string) (bool, error) {
	s, err := g.out(ctx, dir, "status", "--porcelain")
	return s == "", err
}

// Ahead fetches base from origin and counts the commits on HEAD that origin/base lacks.
func (g Git) Ahead(ctx context.Context, dir, base string) (int, error) {
	if _, err := g.out(ctx, dir, "fetch", "-q", "origin", base); err != nil {
		return 0, err
	}
	s, err := g.out(ctx, dir, "rev-list", "--count", "origin/"+base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

func (g Git) HeadSHA(ctx context.Context, dir string) (string, error) {
	return g.out(ctx, dir, "rev-parse", "HEAD")
}

// RemoteSHA returns the commit origin holds for branch, or "" when origin has no such branch.
func (g Git) RemoteSHA(ctx context.Context, dir, branch string) (string, error) {
	s, err := g.out(ctx, dir, "ls-remote", "origin", "refs/heads/"+branch)
	if f := strings.Fields(s); err == nil && len(f) > 0 {
		return f[0], nil
	}
	return "", err
}

// Push publishes HEAD as branch on origin, never forcing. A rejection because origin's branch holds
// commits HEAD lacks returns ports.ErrDiverged.
func (g Git) Push(ctx context.Context, dir, branch string) error {
	_, err := g.out(ctx, dir, "push", "-q", "-u", "origin", "HEAD:refs/heads/"+branch)
	if e, ok := errors.AsType[*run.Error](err); ok && strings.Contains(e.Stderr, "[rejected]") {
		return fmt.Errorf("%w: %w", ports.ErrDiverged, err)
	}
	return err
}

func (g Git) Subject(ctx context.Context, dir string) (string, error) {
	return g.out(ctx, dir, "log", "-1", "--format=%s")
}
