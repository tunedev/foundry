// Package github implements the forge port with the gh CLI.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/run"
)

type GitHub struct{ Timeout time.Duration }

func (g GitHub) gh(ctx context.Context, dir, stdin string, args ...string) ([]byte, error) {
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	out, err := run.Cmd(ctx, g.Timeout, dir, in, "gh", args...)
	return out, run.Classify(err)
}

type prJSON struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	Body       string `json:"body"`
	HeadRefOid string `json:"headRefOid"`
}

func (g GitHub) FindPR(ctx context.Context, dir, branch string) (ports.PR, bool, error) {
	out, err := g.gh(ctx, dir, "", "pr", "list", "--head", branch, "--state", "open",
		"--json", "number,url,body,headRefOid", "--limit", "1")
	if err != nil {
		return ports.PR{}, false, err
	}
	var prs []prJSON
	if err := json.Unmarshal(out, &prs); err != nil {
		return ports.PR{}, false, fmt.Errorf("gh pr list: %w", err)
	}
	if len(prs) == 0 {
		return ports.PR{}, false, nil
	}
	p := prs[0]
	return ports.PR{Number: p.Number, URL: p.URL, Body: p.Body, HeadSHA: p.HeadRefOid}, true, nil
}

func (g GitHub) CreatePR(ctx context.Context, dir, base, branch, title, body string) (ports.PR, error) {
	if _, err := g.gh(ctx, dir, body, "pr", "create", "--base", base, "--head", branch, "--title", title, "--body-file", "-"); err != nil {
		return ports.PR{}, err
	}
	pr, found, err := g.FindPR(ctx, dir, branch)
	if err == nil && !found {
		err = fmt.Errorf("gh pr create succeeded but no open pull request lists head %s", branch)
	}
	return pr, err
}

func (g GitHub) EditPRBody(ctx context.Context, dir string, number int, body string) error {
	_, err := g.gh(ctx, dir, body, "pr", "edit", strconv.Itoa(number), "--body-file", "-")
	return err
}

type checkJSON struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

// Checks lists every check on the pull request head. gh exits non-zero while checks fail or are
// pending but still prints them, so its output is read before its error.
func (g GitHub) Checks(ctx context.Context, dir string, number int) ([]ports.Check, error) {
	out, err := g.gh(ctx, dir, "", "pr", "checks", strconv.Itoa(number), "--json", "name,bucket,link")
	var raw []checkJSON
	if jerr := json.Unmarshal(out, &raw); jerr == nil {
		checks := make([]ports.Check, len(raw))
		for i, c := range raw {
			checks[i] = ports.Check{Name: c.Name, Bucket: c.Bucket, Link: c.Link}
		}
		return checks, nil
	}
	if err != nil && strings.Contains(err.Error(), "no checks reported") {
		return nil, nil
	}
	if err == nil {
		err = fmt.Errorf("gh pr checks: unreadable output %q", out)
	}
	return nil, err
}

func (g GitHub) EnsureLabels(ctx context.Context, dir string, labels []string) error {
	for _, l := range labels {
		if _, err := g.gh(ctx, dir, "", "label", "create", l, "--force", "--color", "ededed", "--description", "foundry"); err != nil {
			return err
		}
	}
	return nil
}

func (g GitHub) OpenIssues(ctx context.Context, dir, label string) ([]ports.Issue, error) {
	out, err := g.gh(ctx, dir, "", "issue", "list", "--label", label, "--state", "open", "--json", "number,title,url", "--limit", "500")
	if err != nil {
		return nil, err
	}
	var issues []ports.Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	return issues, nil
}

func (g GitHub) CreateIssue(ctx context.Context, dir string, in ports.NewIssue) (ports.Issue, error) {
	args := []string{"issue", "create", "--title", in.Title, "--body-file", "-"}
	for _, l := range in.Labels {
		args = append(args, "--label", l)
	}
	out, err := g.gh(ctx, dir, in.Body, args...)
	if err != nil {
		return ports.Issue{}, err
	}
	url := strings.TrimSpace(string(out))
	url = url[strings.LastIndex(url, "\n")+1:]
	n, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		return ports.Issue{}, fmt.Errorf("gh issue create: no issue number in %q", url)
	}
	return ports.Issue{Number: n, Title: in.Title, URL: url}, nil
}

func (g GitHub) CommentIssue(ctx context.Context, dir string, number int, body string) error {
	_, err := g.gh(ctx, dir, body, "issue", "comment", strconv.Itoa(number), "--body-file", "-")
	return err
}
