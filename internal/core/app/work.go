package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/core/ship"
	"github.com/tunedev/foundry/internal/core/work"
)

type WorkDeps struct {
	Forge    ports.Forge
	Agent    ports.AgentRunner
	Notifier ports.Notifier
	LastShip func(repo, branch string) (work.ShipRecord, bool, error)
	Rounds   func(repo, branch string) (ship.Rounds, error)
	Now      func() time.Time
}

type WorkInput struct {
	Dir, Repo, Base, RunID, Bot          string
	Verify, UserDataPaths, Trusted       []string
	Window                               work.Window
	ClaimLease, IssueTimeout, RetryPause time.Duration
	MaxIssues, MaxTurns                  int
}

// Work releases stale claims, picks ready friction issues and runs the agent on each in turn, then sends a
// summary when it picked any or ended early. Outside the window it does nothing. A forge failure while
// releasing, picking or claiming ends the run; one issue's failure never stops the next. It sets run.id,
// repo and issues.picked on ctx's span.
func Work(ctx context.Context, d WorkDeps, in WorkInput) (ports.Summary, error) {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("run.id", in.RunID), attribute.String("repo", in.Repo))
	s := ports.Summary{RunID: in.RunID, Repo: in.Repo}
	if !in.Window.Contains(d.Now()) {
		slog.InfoContext(ctx, "outside the work window")
		return s, nil
	}
	start := d.Now()
	err := runWork(ctx, d, in, &s)
	s.Wall = d.Now().Sub(start)
	if err != nil {
		s.Aborted = err.Error()
	}
	if len(s.Issues) > 0 || err != nil {
		step(ctx, "foundry.notify", func(ctx context.Context) error {
			return RetryOnce(ctx, in.RetryPause, func() error { return d.Notifier.Notify(ctx, s) })
		})
	}
	return s, err
}

func runWork(ctx context.Context, d WorkDeps, in WorkInput, s *ports.Summary) error {
	if err := step(ctx, "foundry.claims.release", func(ctx context.Context) error { return releaseClaims(ctx, d, in) }); err != nil {
		return err
	}
	var picked []ports.Issue
	if err := step(ctx, "foundry.pick", func(ctx context.Context) (err error) { picked, err = pick(ctx, d, in); return err }); err != nil {
		return err
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.Int("issues.picked", len(picked)))
	if len(picked) == 0 {
		return nil
	}
	if err := RetryOnce(ctx, in.RetryPause, func() error { return d.Forge.EnsureLabels(ctx, in.Dir, friction.AllLabels) }); err != nil {
		return err
	}
	for _, is := range picked {
		if !in.Window.Contains(d.Now()) {
			slog.InfoContext(ctx, "work window closed", "remaining", len(picked)-len(s.Issues))
			break
		}
		if err := relabel(ctx, d, in, is.Number, work.ClaimComment(in.RunID), []string{friction.InProgress}, nil); err != nil {
			return err
		}
		rep, cost := runIssue(ctx, d, in, is)
		s.Issues = append(s.Issues, rep)
		s.CostUSD += cost
	}
	return nil
}

func releaseClaims(ctx context.Context, d WorkDeps, in WorkInput) error {
	var claimed []ports.Issue
	if err := RetryOnce(ctx, in.RetryPause, func() (err error) { claimed, err = d.Forge.OpenIssues(ctx, in.Dir, friction.InProgress); return err }); err != nil {
		return err
	}
	for _, is := range claimed {
		c, ok := work.CurrentClaim(is.Comments, in.Bot)
		var pr ports.PR
		var found bool
		if ok {
			branch := work.Branch(is.Number, c.RunID)
			if err := RetryOnce(ctx, in.RetryPause, func() (err error) { pr, found, err = d.Forge.BranchPR(ctx, in.Dir, branch); return err }); err != nil {
				return err
			}
		}
		var err error
		switch work.Decide(c, ok, pr, found, d.Now(), in.ClaimLease) {
		case work.Rejected:
			err = relabel(ctx, d, in, is.Number, "foundry: "+pr.URL+" was closed without merging, so this needs a decision.",
				[]string{friction.NeedsDecision.Label()}, []string{friction.InProgress, friction.Ready.Label()})
		case work.Expired:
			err = relabel(ctx, d, in, is.Number, "foundry: the claim by run "+c.RunID+" expired with no pull request; back in the queue.",
				nil, []string{friction.InProgress})
		case work.NoClaim:
			err = relabel(ctx, d, in, is.Number, "foundry: in progress with no claim; back in the queue.", nil, []string{friction.InProgress})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func pick(ctx context.Context, d WorkDeps, in WorkInput) ([]ports.Issue, error) {
	var ready []ports.Issue
	var open []ports.PR
	if err := RetryOnce(ctx, in.RetryPause, func() (err error) { ready, err = d.Forge.OpenIssues(ctx, in.Dir, friction.Ready.Label()); return err }); err != nil {
		return nil, err
	}
	if err := RetryOnce(ctx, in.RetryPause, func() (err error) { open, err = d.Forge.OpenPRs(ctx, in.Dir); return err }); err != nil {
		return nil, err
	}
	return work.Pick(ready, open, in.MaxIssues, in.Trusted), nil
}

// relabel comments, then edits labels, each retried once on a transient failure. A crash between the two
// leaves the labels as they were.
func relabel(ctx context.Context, d WorkDeps, in WorkInput, n int, body string, add, remove []string) error {
	if err := comment(ctx, d, in, n, body); err != nil {
		return err
	}
	return RetryOnce(ctx, in.RetryPause, func() error { return d.Forge.EditLabels(ctx, in.Dir, n, add, remove) })
}

// comment posts body on issue n. A retry first reads the issue's comments and skips the write when the bot
// already posted this body, as when the first attempt's response was lost.
func comment(ctx context.Context, d WorkDeps, in WorkInput, n int, body string) error {
	attempted := false
	return RetryOnce(ctx, in.RetryPause, func() error {
		if attempted {
			cs, err := d.Forge.IssueComments(ctx, in.Dir, n)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(cs, func(c ports.Comment) bool {
				return strings.EqualFold(c.Author, in.Bot) && strings.TrimSpace(c.Body) == strings.TrimSpace(body)
			}) {
				return nil
			}
		}
		attempted = true
		return d.Forge.CommentIssue(ctx, in.Dir, n, body)
	})
}

func runIssue(ctx context.Context, d WorkDeps, in WorkInput, is ports.Issue) (ports.IssueReport, float64) {
	branch := work.Branch(is.Number, in.RunID)
	var o work.Outcome
	var res ports.AgentResult
	err := step(ctx, "foundry.issue", func(ctx context.Context) error {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.Int("issue", is.Number), attribute.String("branch", branch))
		o, res = runAgent(ctx, d, in, is, branch)
		span.SetAttributes(attribute.String("outcome", string(o.Kind)), attribute.String("reason", string(o.Reason)))
		return applyOutcome(ctx, d, in, is.Number, o)
	})
	issueOutcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("repo", in.Repo),
		attribute.String("outcome", string(o.Kind)), attribute.String("reason", string(o.Reason))))
	agentCost.Add(ctx, res.CostUSD, metric.WithAttributes(attribute.String("repo", in.Repo), attribute.String("model", res.Model)))
	if r, rerr := d.Rounds(in.Repo, branch); rerr == nil {
		reviewRounds.Record(ctx, int64(r.Count), metric.WithAttributes(attribute.String("repo", in.Repo)))
	}
	rep := ports.IssueReport{Number: is.Number, Title: is.Title, URL: is.URL, Outcome: string(o.Kind), Reason: string(o.Reason),
		PRURL: o.PRURL, Detail: o.Detail}
	if err != nil {
		rep.Detail += "; issue not updated: " + err.Error()
	}
	return rep, res.CostUSD
}

func runAgent(ctx context.Context, d WorkDeps, in WorkInput, is ports.Issue, branch string) (work.Outcome, ports.AgentResult) {
	actx, cancel := context.WithTimeout(ctx, in.IssueTimeout)
	defer cancel()
	res, err := d.Agent.Run(actx, ports.AgentJob{RepoDir: in.Dir, Base: in.Base, Branch: branch,
		Prompt: work.Prompt(is, in.Verify), Verify: in.Verify, MaxTurns: in.MaxTurns})
	rec, shipped, lerr := d.LastShip(in.Repo, branch)
	if lerr != nil {
		slog.ErrorContext(ctx, "ship record unreadable", "branch", branch, "error", lerr.Error())
	}
	timedOut := errors.Is(actx.Err(), context.DeadlineExceeded)
	return work.Classify(work.Evidence{Result: res, RunErr: err, TimedOut: timedOut, Ship: rec, Shipped: shipped}), res
}

func applyOutcome(ctx context.Context, d WorkDeps, in WorkInput, n int, o work.Outcome) error {
	switch o.Kind {
	case work.ReadyForHuman:
		return comment(ctx, d, in, n, "foundry: "+o.PRURL+" is ready for review.")
	case work.NeedsDecision:
		return relabel(ctx, d, in, n, "foundry: this needs a decision.\n\n"+publicSafe(o.Detail, in.UserDataPaths),
			[]string{friction.NeedsDecision.Label()}, []string{friction.InProgress, friction.Ready.Label()})
	}
	return relabel(ctx, d, in, n, fmt.Sprintf("foundry: run %s stuck (%s).\n\n%s", in.RunID, o.Reason, publicSafe(o.Detail, in.UserDataPaths)),
		[]string{friction.Stuck}, []string{friction.InProgress})
}

// publicSafe returns text for a public comment, at most 4000 bytes cut on a character boundary, or a note
// that it was withheld.
func publicSafe(text string, userDataPaths []string) string {
	if why := friction.Unsafe(text, userDataPaths); len(why) > 0 {
		return "Details withheld: not public-safe (" + strings.Join(why, ", ") + "). See the run's trace."
	}
	return strings.ToValidUTF8(text[:min(len(text), 4000)], "")
}
