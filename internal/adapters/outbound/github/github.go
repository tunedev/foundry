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

// GitHub runs gh with Token as GH_TOKEN when it is set, and with gh's own login otherwise.
type GitHub struct {
	Timeout time.Duration
	Token   string
}

func (g GitHub) gh(ctx context.Context, dir, stdin string, args ...string) ([]byte, error) {
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	var env []string
	if g.Token != "" {
		env = []string{"GH_TOKEN=" + g.Token}
	}
	out, err := run.CmdEnv(ctx, g.Timeout, dir, env, in, "gh", args...)
	return out, run.Classify(err)
}

const prFields = "number,url,body,headRefOid,headRefName,state"

type prJSON struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	Body        string `json:"body"`
	HeadRefOid  string `json:"headRefOid"`
	HeadRefName string `json:"headRefName"`
	State       string `json:"state"`
}

func (g GitHub) listPRs(ctx context.Context, dir string, args ...string) ([]ports.PR, error) {
	out, err := g.gh(ctx, dir, "", append([]string{"pr", "list", "--json", prFields}, args...)...)
	if err != nil {
		return nil, err
	}
	var raw []prJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	prs := make([]ports.PR, len(raw))
	for i, p := range raw {
		prs[i] = ports.PR{Number: p.Number, URL: p.URL, Body: p.Body, HeadSHA: p.HeadRefOid, Head: p.HeadRefName, State: p.State}
	}
	return prs, nil
}

func first(prs []ports.PR, err error) (ports.PR, bool, error) {
	if err != nil || len(prs) == 0 {
		return ports.PR{}, false, err
	}
	return prs[0], true, nil
}

func (g GitHub) FindPR(ctx context.Context, dir, branch string) (ports.PR, bool, error) {
	return first(g.listPRs(ctx, dir, "--head", branch, "--state", "open", "--limit", "1"))
}

func (g GitHub) BranchPR(ctx context.Context, dir, branch string) (ports.PR, bool, error) {
	return first(g.listPRs(ctx, dir, "--head", branch, "--state", "all", "--limit", "1"))
}

func (g GitHub) OpenPRs(ctx context.Context, dir string) ([]ports.PR, error) {
	return g.listPRs(ctx, dir, "--state", "open", "--limit", "200")
}

func (g GitHub) EditLabels(ctx context.Context, dir string, number int, add, remove []string) error {
	args := []string{"issue", "edit", strconv.Itoa(number)}
	for _, l := range add {
		args = append(args, "--add-label", l)
	}
	for _, l := range remove {
		args = append(args, "--remove-label", l)
	}
	_, err := g.gh(ctx, dir, "", args...)
	return err
}

type author struct {
	Login string `json:"login"`
}

type commentJSON struct {
	Author    author    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

func (c commentJSON) comment() ports.Comment {
	return ports.Comment{Author: c.Author.Login, Body: c.Body, CreatedAt: c.CreatedAt}
}

func (g GitHub) IssueComments(ctx context.Context, dir string, number int) ([]ports.Comment, error) {
	out, err := g.gh(ctx, dir, "", "issue", "view", strconv.Itoa(number), "--json", "comments")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Comments []commentJSON `json:"comments"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh issue view: %w", err)
	}
	cs := make([]ports.Comment, len(raw.Comments))
	for i, c := range raw.Comments {
		cs[i] = c.comment()
	}
	return cs, nil
}

type issueJSON struct {
	Author    author    `json:"author"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Comments []commentJSON `json:"comments"`
}

func (g GitHub) OpenIssues(ctx context.Context, dir, label string) ([]ports.Issue, error) {
	out, err := g.gh(ctx, dir, "", "issue", "list", "--label", label, "--state", "open",
		"--json", "number,title,url,author,body,createdAt,labels,comments", "--limit", "500")
	if err != nil {
		return nil, err
	}
	var raw []issueJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	issues := make([]ports.Issue, len(raw))
	for i, r := range raw {
		is := ports.Issue{Number: r.Number, Title: r.Title, URL: r.URL, Author: r.Author.Login, Body: r.Body, CreatedAt: r.CreatedAt}
		for _, l := range r.Labels {
			is.Labels = append(is.Labels, l.Name)
		}
		for _, c := range r.Comments {
			is.Comments = append(is.Comments, c.comment())
		}
		issues[i] = is
	}
	return issues, nil
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
