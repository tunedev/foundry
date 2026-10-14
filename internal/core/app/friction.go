package app

import (
	"context"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
)

type FrictionDeps struct {
	Forge ports.Forge
	Load  func(repo, branch string) (friction.Answer, error)
	Save  func(repo, branch string, a friction.Answer) error
}

type FileInput struct {
	Dir, Repo, Branch, Title, Body string
	Kind                           friction.Kind
	UserDataPaths                  []string
	Max                            int
	RetryPause                     time.Duration
}

// Filed is the issue a report landed on. Duplicate means an open issue already named the friction and got a comment.
type Filed struct {
	Issue     ports.Issue
	Duplicate bool
}

// FileFriction validates a report, then comments on a similar open friction issue or creates a labelled one,
// and records the issue against the branch.
func FileFriction(ctx context.Context, d FrictionDeps, in FileInput) (Filed, error) {
	if err := friction.ValidateBody(in.Body); err != nil {
		return Filed{}, needs("friction", "%v", err)
	}
	if why := friction.Unsafe(in.Title+"\n"+in.Body, in.UserDataPaths); len(why) > 0 {
		return Filed{}, needs("friction", "report is not public-safe (%s); describe the problem without user data or secrets", strings.Join(why, ", "))
	}
	a, err := d.Load(in.Repo, in.Branch)
	if err != nil {
		return Filed{}, err
	}
	if len(a.Issues) >= in.Max {
		return Filed{}, needs("friction", "this branch already reported %d friction issues, the maximum", len(a.Issues))
	}
	var f Filed
	err = step(ctx, "file friction", func() error {
		return retryOnce(ctx, in.RetryPause, func() (err error) {
			f, err = fileOnce(ctx, d.Forge, in)
			return err
		})
	})
	if err != nil {
		return f, err
	}
	a.Issues = append(a.Issues, f.Issue.Number)
	a.None = ""
	return f, d.Save(in.Repo, in.Branch, a)
}

// fileOnce lists open friction issues before writing, so a retry after a lost response comments
// on the issue the first attempt created instead of creating a second one.
func fileOnce(ctx context.Context, forge ports.Forge, in FileInput) (Filed, error) {
	if err := forge.EnsureLabels(ctx, in.Dir, friction.AllLabels); err != nil {
		return Filed{}, err
	}
	open, err := forge.OpenIssues(ctx, in.Dir, friction.Label)
	if err != nil {
		return Filed{}, err
	}
	if m, ok := friction.Match(in.Title, open); ok {
		return Filed{Issue: m, Duplicate: true}, forge.CommentIssue(ctx, in.Dir, m.Number, "Hit again from branch `"+in.Branch+"`.\n\n"+in.Body)
	}
	issue, err := forge.CreateIssue(ctx, in.Dir, ports.NewIssue{Title: in.Title, Body: in.Body, Labels: []string{friction.Label, in.Kind.Label()}})
	return Filed{Issue: issue}, err
}

// NoFriction records that branch hit no friction worth reporting.
func NoFriction(d FrictionDeps, repo, branch, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return needs("friction", "--reason is required")
	}
	a, err := d.Load(repo, branch)
	if err != nil {
		return err
	}
	a.None = reason
	return d.Save(repo, branch, a)
}
