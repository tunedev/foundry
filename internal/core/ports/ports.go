// Package ports declares what the core needs from a code forge, an agent runner and a notifier.
package ports

import (
	"context"
	"errors"
	"time"
)

// ErrTransient marks a failure worth one retry: a timeout, a network error, a forge 5xx or rate limit.
var ErrTransient = errors.New("transient")

// ErrDiverged marks a push the remote rejected because its branch holds commits HEAD lacks.
var ErrDiverged = errors.New("remote branch has diverged")

// ErrFindings marks a review gate that denied the code. Its message carries the findings.
var ErrFindings = errors.New("review findings")

type PR struct {
	Number  int
	URL     string
	Body    string
	HeadSHA string
	Head    string
	// State is OPEN, CLOSED or MERGED.
	State string
}

// Check is one CI check on a pull request head. Bucket is pass, fail, pending, skipping or cancel.
type Check struct {
	Name   string
	Bucket string
	Link   string
}

type Comment struct {
	Author, Body string
	CreatedAt    time.Time
}

type Issue struct {
	Number    int
	Title     string
	URL       string
	Author    string
	Body      string
	Labels    []string
	CreatedAt time.Time
	Comments  []Comment
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
	// BranchPR returns the newest pull request whose head is branch, in any state.
	BranchPR(ctx context.Context, dir, branch string) (PR, bool, error)
	OpenPRs(ctx context.Context, dir string) ([]PR, error)
	EditLabels(ctx context.Context, dir string, number int, add, remove []string) error
	IssueComments(ctx context.Context, dir string, number int) ([]Comment, error)
}

// AgentJob is one issue for an agent: a fresh Branch from origin/Base of the repo checked out at RepoDir.
type AgentJob struct {
	RepoDir, Base, Branch, Prompt string
	Verify                        []string
	MaxTurns                      int
}

// AgentResult is what the agent reported when it finished. Subtype is Claude Code's result subtype,
// such as success or error_max_turns.
type AgentResult struct {
	Text, Subtype, SessionID, Model string
	Turns                           int
	CostUSD                         float64
}

// AgentRunner runs an agent on one job until it finishes or ctx ends.
type AgentRunner interface {
	Run(ctx context.Context, job AgentJob) (AgentResult, error)
}

type IssueReport struct {
	Number                                     int
	Title, URL, Outcome, Reason, PRURL, Detail string
}

// Summary is one work run, for the human. Aborted says why the run ended early, or is empty.
type Summary struct {
	RunID, Repo string
	Issues      []IssueReport
	CostUSD     float64
	Wall        time.Duration
	Aborted     string
}

// Notifier sends a run summary to the human's phone channel.
type Notifier interface {
	Notify(ctx context.Context, s Summary) error
}
