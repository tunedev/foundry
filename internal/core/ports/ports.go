// Package ports declares what the core needs from a code forge.
package ports

import (
	"context"
	"errors"
)

// ErrTransient marks a failure worth one retry: a timeout, a network error, a forge 5xx or rate limit.
var ErrTransient = errors.New("transient")

type PR struct {
	Number  int
	URL     string
	Body    string
	HeadSHA string
}

// Check is one CI check on a pull request head. Bucket is pass, fail, pending, skipping or cancel.
type Check struct {
	Name   string
	Bucket string
	Link   string
}

type Issue struct {
	Number int
	Title  string
	URL    string
}

type NewIssue struct {
	Title  string
	Body   string
	Labels []string
}

// Forge is the code host: pull requests, their checks, and issues. dir is a checkout of the repo.
type Forge interface {
	FindPR(ctx context.Context, dir, branch string) (PR, bool, error)
	CreatePR(ctx context.Context, dir, base, branch, title, body string) (PR, error)
	EditPRBody(ctx context.Context, dir string, number int, body string) error
	Checks(ctx context.Context, dir string, number int) ([]Check, error)
	EnsureLabels(ctx context.Context, dir string, labels []string) error
	OpenIssues(ctx context.Context, dir, label string) ([]Issue, error)
	CreateIssue(ctx context.Context, dir string, in NewIssue) (Issue, error)
	CommentIssue(ctx context.Context, dir string, number int, body string) error
}
