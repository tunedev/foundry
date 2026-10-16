package app

import (
	"context"
	"slices"
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
	var created []ports.Issue
	err = step(ctx, "foundry.friction.file", func(ctx context.Context) error {
		return retryOnce(ctx, in.RetryPause, func() (err error) {
			f, created, err = fileOnce(ctx, d.Forge, in, created)
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

// fileOnce lists open friction issues before writing. beforeCreate is the list an earlier attempt saw
// before it tried to create the issue, or nil when none did. An open issue with exactly this title that
// was not in that list is the one the earlier attempt created, so it is returned as filed, with no comment.
// It returns the list it saw when it tries to create the issue.
func fileOnce(ctx context.Context, forge ports.Forge, in FileInput, beforeCreate []ports.Issue) (Filed, []ports.Issue, error) {
	if err := forge.EnsureLabels(ctx, in.Dir, friction.AllLabels); err != nil {
		return Filed{}, beforeCreate, err
	}
	open, err := forge.OpenIssues(ctx, in.Dir, friction.Label)
	if err != nil {
		return Filed{}, beforeCreate, err
	}
	if beforeCreate != nil {
		for _, is := range open {
			if is.Title == in.Title && !slices.ContainsFunc(beforeCreate, func(b ports.Issue) bool { return b.Number == is.Number }) {
				return Filed{Issue: is}, beforeCreate, nil
			}
		}
	}
	if m, ok := friction.Match(in.Title, open); ok {
		return Filed{Issue: m, Duplicate: true}, beforeCreate, forge.CommentIssue(ctx, in.Dir, m.Number, friction.HitAgain+" `"+in.Branch+"`.\n\n"+in.Body)
	}
	issue, err := forge.CreateIssue(ctx, in.Dir, ports.NewIssue{Title: in.Title, Body: in.Body, Labels: []string{friction.Label, in.Kind.Label()}})
	return Filed{Issue: issue}, append([]ports.Issue{}, open...), err
}

// NoFriction records that branch hit no friction worth reporting.
func NoFriction(d FrictionDeps, repo, branch, reason string) error {
	a, err := d.Load(repo, branch)
	if err != nil {
		return err
	}
	a.None = reason
	return d.Save(repo, branch, a)
}
