package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/core/ship"
)

// ShipDeps are the concrete steps cmd/foundry injects. Forge is the one port with a second implementation.
type ShipDeps struct {
	Forge      ports.Forge
	Branch     func(ctx context.Context, dir string) (string, error)
	Clean      func(ctx context.Context, dir string) (bool, error)
	Ahead      func(ctx context.Context, dir, base string) (int, error)
	HeadSHA    func(ctx context.Context, dir string) (string, error)
	RemoteSHA  func(ctx context.Context, dir, branch string) (string, error)
	Push       func(ctx context.Context, dir, branch string) error
	Subject    func(ctx context.Context, dir string) (string, error)
	RunCheck   func(ctx context.Context, dir, command string) error
	Review     func(ctx context.Context, dir string) error
	Answer     func(repo, branch string) (friction.Answer, error)
	Rounds     func(repo, branch string) (ship.Rounds, error)
	SaveRounds func(repo, branch string, r ship.Rounds) error
}

type ShipInput struct {
	Dir, Repo, Base               string
	Checks                        []string
	Recap, Title                  string
	CITimeout, CIPoll, RetryPause time.Duration
	MaxReviewRounds               int
}

// Report is how far a ship run got: the steps that passed and the step it stopped at.
type Report struct {
	Repo, Branch string
	Passed       []string
	Step         string
	PRURL        string
}

const HandOff = "hand-off"

// Ship moves the branch checked out at in.Dir through every step up to a green pull request. Each step
// reads current state, so a rerun resumes at the first unmet step and makes no remote writes once all
// steps are met. It returns a *NeedsAction for a stop the agent fixes, and an ErrTransient error when a
// transient failure survived its retry.
func Ship(ctx context.Context, d ShipDeps, in ShipInput) (Report, error) {
	r := Report{Repo: in.Repo}
	var head string
	var pr ports.PR
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"preflight", func(ctx context.Context) (err error) { r.Branch, err = preflight(ctx, d, in); return err }},
		{"checks", func(ctx context.Context) error { return runChecks(ctx, d, in) }},
		{"review", func(ctx context.Context) error { return review(ctx, d, in, r.Branch) }},
		{"push-pr", func(ctx context.Context) (err error) {
			head, pr, err = pushAndPR(ctx, d, in, r.Branch)
			r.PRURL = pr.URL
			return err
		}},
		{"ci", func(ctx context.Context) error { return waitCI(ctx, d, in, r.Branch, head, pr) }},
		{"friction", func(context.Context) error { return frictionGiven(d, in.Repo, r.Branch) }},
	}
	for _, s := range steps {
		r.Step = s.name
		if err := step(ctx, "foundry.ship."+strings.ReplaceAll(s.name, "-", "_"), s.fn); err != nil {
			return r, err
		}
		r.Passed = append(r.Passed, s.name)
	}
	r.Step = HandOff
	return r, nil
}

func preflight(ctx context.Context, d ShipDeps, in ShipInput) (string, error) {
	branch, err := d.Branch(ctx, in.Dir)
	if err != nil {
		return "", err
	}
	if branch == in.Base || branch == "HEAD" {
		return branch, needs("preflight", "on %s; ship from a feature branch", branch)
	}
	clean, err := d.Clean(ctx, in.Dir)
	if err != nil {
		return branch, err
	}
	if !clean {
		return branch, needs("preflight", "uncommitted changes; commit or stash them, then rerun")
	}
	var ahead int
	if err := RetryOnce(ctx, in.RetryPause, func() (err error) { ahead, err = d.Ahead(ctx, in.Dir, in.Base); return err }); err != nil {
		return branch, err
	}
	if ahead == 0 {
		return branch, needs("preflight", "no commits ahead of origin/%s", in.Base)
	}
	return branch, nil
}

func runChecks(ctx context.Context, d ShipDeps, in ShipInput) error {
	for _, c := range in.Checks {
		if err := d.RunCheck(ctx, in.Dir, c); err != nil {
			return needs("checks", "%s failed; fix it, commit, rerun:\n%v", c, err)
		}
	}
	return nil
}

// review runs vouch on HEAD. A review that ends in findings on a commit not counted before adds one round
// for the branch; the round that reaches in.MaxReviewRounds stops with ship.NotConverged instead of asking
// for another fix.
func review(ctx context.Context, d ShipDeps, in ShipInput, branch string) error {
	err := RetryOnce(ctx, in.RetryPause, func() error { return d.Review(ctx, in.Dir) })
	switch {
	case err == nil || isTransient(err):
		return err
	case errors.Is(err, ports.ErrFindings):
		r, rerr := countRound(ctx, d, in, branch)
		if rerr != nil {
			return rerr
		}
		trace.SpanFromContext(ctx).SetAttributes(findingAttrs(r.Count, err)...)
		if r.Count >= in.MaxReviewRounds {
			return needs("review", "%s after %d rounds; report the last findings to the user and stop "+
				"(to allow more, rerun with FOUNDRY_MAX_REVIEW_ROUNDS above %d):\n%v", ship.NotConverged, r.Count, r.Count, err)
		}
		return needs("review", "vouch did not pass this code; fix the findings, commit, rerun:\n%v", err)
	}
	return needs("review", "vouch failed to run; report this error to the user:\n%v", err)
}

// findingAttrs is the review round and the count of findings per severity, in severity order.
func findingAttrs(round int, err error) []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.Int("round", round)}
	if fe, ok := errors.AsType[*ports.FindingsError](err); ok {
		for _, sev := range slices.Sorted(maps.Keys(fe.Counts)) {
			attrs = append(attrs, attribute.Int("findings."+sev, fe.Counts[sev]))
		}
	}
	return attrs
}

// countRound adds one review round for branch unless HEAD is the commit the last round already counted.
func countRound(ctx context.Context, d ShipDeps, in ShipInput, branch string) (ship.Rounds, error) {
	r, err := d.Rounds(in.Repo, branch)
	if err != nil {
		return r, err
	}
	head, err := d.HeadSHA(ctx, in.Dir)
	if err != nil || head == r.LastSHA {
		return r, err
	}
	r = ship.Rounds{Count: r.Count + 1, LastSHA: head}
	return r, d.SaveRounds(in.Repo, branch, r)
}

// pushAndPR pushes HEAD when origin lacks it and makes sure an open pull request carries a valid recap.
// It edits the body only when the agent passed a different recap, and checks for an existing pull
// request before every create, so a retried create never opens a second one.
func pushAndPR(ctx context.Context, d ShipDeps, in ShipInput, branch string) (string, ports.PR, error) {
	var pr ports.PR
	head, err := d.HeadSHA(ctx, in.Dir)
	if err != nil {
		return "", pr, err
	}
	var remote string
	if err := RetryOnce(ctx, in.RetryPause, func() (err error) { remote, err = d.RemoteSHA(ctx, in.Dir, branch); return err }); err != nil {
		return head, pr, err
	}
	if remote != head {
		err := RetryOnce(ctx, in.RetryPause, func() error { return d.Push(ctx, in.Dir, branch) })
		if errors.Is(err, ports.ErrDiverged) {
			return head, pr, needs("push-pr", "origin/%s has commits HEAD lacks: the branch was rebased or amended here, "+
				"or someone else pushed to it. foundry never force-pushes; ask the user whether to overwrite origin/%s", branch, branch)
		}
		if err != nil {
			return head, pr, err
		}
	}
	var found bool
	find := func() (err error) { pr, found, err = d.Forge.FindPR(ctx, in.Dir, branch); return err }
	if err := RetryOnce(ctx, in.RetryPause, find); err != nil {
		return head, pr, err
	}
	switch {
	case !found:
		if err := recapReady(in.Recap); err != nil {
			return head, pr, err
		}
		title := in.Title
		if title == "" {
			if title, err = d.Subject(ctx, in.Dir); err != nil {
				return head, pr, err
			}
		}
		create := func() error {
			if err := find(); err != nil || found {
				return err
			}
			pr, err = d.Forge.CreatePR(ctx, in.Dir, in.Base, branch, title, in.Recap)
			return err
		}
		return head, pr, RetryOnce(ctx, in.RetryPause, create)
	case in.Recap != "" && !sameBody(in.Recap, pr.Body):
		if err := recapReady(in.Recap); err != nil {
			return head, pr, err
		}
		err := RetryOnce(ctx, in.RetryPause, func() error { return d.Forge.EditPRBody(ctx, in.Dir, pr.Number, in.Recap) })
		pr.Body = in.Recap
		return head, pr, err
	}
	if err := ship.ValidateRecap(pr.Body); err != nil {
		return head, pr, needs("push-pr", "PR #%d body: %v; pass --recap <file> to replace it", pr.Number, err)
	}
	return head, pr, nil
}

func recapReady(recap string) error {
	if recap == "" {
		return needs("push-pr", "no PR yet; write the system recap to a file with headings %s and pass --recap <file>",
			strings.Join(ship.RecapHeadings, ", "))
	}
	if err := ship.ValidateRecap(recap); err != nil {
		return needs("push-pr", "%v", err)
	}
	return nil
}

func sameBody(a, b string) bool {
	norm := func(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }
	return norm(a) == norm(b)
}

// waitCI polls the pull request until the forge reports head as its head and every check on it is green,
// a check fails, or in.CITimeout passes.
func waitCI(ctx context.Context, d ShipDeps, in ShipInput, branch, head string, pr ports.PR) error {
	deadline := time.Now().Add(in.CITimeout)
	for {
		if pr.HeadSHA == head {
			var checks []ports.Check
			if err := RetryOnce(ctx, in.RetryPause, func() (err error) { checks, err = d.Forge.Checks(ctx, in.Dir, pr.Number); return err }); err != nil {
				return err
			}
			switch state, red := ship.Verdict(checks); state {
			case ship.CIGreen:
				return nil
			case ship.CIRed:
				return needs("ci", "failing checks on %s; fix them, commit, rerun:\n%s", short(head), describe(red))
			}
		}
		if time.Now().After(deadline) {
			return needs("ci", "checks on %s still pending after %s; rerun foundry ship", short(head), in.CITimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(in.CIPoll):
		}
		if pr.HeadSHA != head {
			if err := RetryOnce(ctx, in.RetryPause, func() (err error) { pr, _, err = d.Forge.FindPR(ctx, in.Dir, branch); return err }); err != nil {
				return err
			}
		}
	}
}

func describe(checks []ports.Check) string {
	var b strings.Builder
	for _, c := range checks {
		fmt.Fprintf(&b, "- %s (%s) %s\n", c.Name, c.Bucket, c.Link)
	}
	return strings.TrimSpace(b.String())
}

func short(sha string) string { return sha[:min(7, len(sha))] }

func frictionGiven(d ShipDeps, repo, branch string) error {
	a, err := d.Answer(repo, branch)
	if err != nil || a.Given() {
		return err
	}
	return needs("friction", "report what slowed you down on this branch: foundry friction file --title <t> --body-file <f> "+
		"--kind ready|needs-decision|cleanup, or foundry friction none --reason <r>")
}
