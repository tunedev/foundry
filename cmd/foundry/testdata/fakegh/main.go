// Command fakegh stands in for gh in foundry's end-to-end tests. Its state is the JSON file named by
// FAKE_STATE. fail_once fails a command before it writes; lose_once fails it after it writes, as when
// a response is lost. user names the account that posts comments (default foundry-bot). A pull request's
// head is read from the checkout's origin, as GitHub would report it; cross marks a pull request from a fork.
// pr list honours --head, --state open and --limit. Commands handled: api user (prints user), pr list, pr create,
// pr edit, pr checks, label create, issue list, issue view, issue create, issue edit, issue comment.
package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

type pr struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Body   string `json:"body"`
	Head   string `json:"head"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Cross  bool   `json:"cross"`
}

type issue struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	Body      string   `json:"body"`
	Labels    []string `json:"labels"`
	Author    string   `json:"author"`
	CreatedAt string   `json:"created_at"`
}

type comment struct {
	Issue     int    `json:"issue"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
}

type check struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

type state struct {
	PRs         []pr              `json:"prs"`
	Issues      []issue           `json:"issues"`
	Comments    []comment         `json:"comments"`
	Labels      []string          `json:"labels"`
	Checks      [][]check         `json:"checks"`
	ChecksCalls int               `json:"checks_calls"`
	FailOnce    map[string]string `json:"fail_once"`
	LoseOnce    map[string]string `json:"lose_once"`
	Writes      int               `json:"writes"`
	User        string            `json:"user"`
}

func main() {
	path := os.Getenv("FAKE_STATE")
	var s state
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &s)
	}
	args := os.Args[1:]
	cmd := strings.Join(args[:min(2, len(args))], " ")
	if msg, ok := s.FailOnce[cmd]; ok {
		delete(s.FailOnce, cmd)
		save(path, s)
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
	code := handle(&s, cmd, args[min(2, len(args)):])
	if msg, ok := s.LoseOnce[cmd]; ok && code == 0 {
		delete(s.LoseOnce, cmd)
		fmt.Fprintln(os.Stderr, msg)
		code = 1
	}
	save(path, s)
	os.Exit(code)
}

func handle(s *state, cmd string, rest []string) int {
	switch cmd {
	case "pr list":
		head, st := flagVal(rest, "--head"), flagVal(rest, "--state")
		limit, _ := strconv.Atoi(cmp.Or(flagVal(rest, "--limit"), "30"))
		out := []map[string]any{}
		for i := len(s.PRs) - 1; i >= 0 && len(out) < limit; i-- {
			p := s.PRs[i]
			state := cmp.Or(p.State, "OPEN")
			if head != "" && p.Head != head || st == "open" && state != "OPEN" {
				continue
			}
			out = append(out, map[string]any{"number": p.Number, "url": p.URL, "body": p.Body,
				"headRefOid": remoteHead(p.Head), "headRefName": p.Head, "state": state, "isCrossRepository": p.Cross})
		}
		printJSON(out)
	case "api user":
		fmt.Println(cmp.Or(s.User, "foundry-bot"))
	case "pr create":
		n := len(s.PRs) + 1
		p := pr{Number: n, URL: fmt.Sprintf("https://github.com/acme/widget/pull/%d", n), Body: stdin(),
			Head: flagVal(rest, "--head"), Title: flagVal(rest, "--title")}
		s.PRs = append(s.PRs, p)
		s.Writes++
		fmt.Println(p.URL)
	case "pr edit":
		n, _ := strconv.Atoi(rest[0])
		s.PRs[n-1].Body = stdin()
		s.Writes++
	case "pr checks":
		cs := []check{{Name: "build", Bucket: "pass"}}
		if len(s.Checks) > 0 {
			cs = s.Checks[min(s.ChecksCalls, len(s.Checks)-1)]
		}
		s.ChecksCalls++
		printJSON(cs)
		for _, c := range cs {
			if c.Bucket == "fail" {
				return 1
			}
		}
		for _, c := range cs {
			if c.Bucket == "pending" {
				return 8
			}
		}
	case "label create":
		if !slices.Contains(s.Labels, rest[0]) {
			s.Labels = append(s.Labels, rest[0])
		}
		s.Writes++
	case "issue list":
		label := flagVal(rest, "--label")
		out := []map[string]any{}
		for _, is := range s.Issues {
			if !slices.Contains(is.Labels, label) {
				continue
			}
			labels, comments := []map[string]any{}, []map[string]any{}
			for _, l := range is.Labels {
				labels = append(labels, map[string]any{"name": l})
			}
			for _, c := range s.Comments {
				if c.Issue == is.Number {
					comments = append(comments, commentOut(c))
				}
			}
			out = append(out, map[string]any{"number": is.Number, "title": is.Title, "url": is.URL, "body": is.Body,
				"author":    map[string]any{"login": cmp.Or(is.Author, "acme")},
				"createdAt": cmp.Or(is.CreatedAt, "2026-01-01T00:00:00Z"), "labels": labels, "comments": comments})
		}
		printJSON(out)
	case "issue view":
		n, _ := strconv.Atoi(rest[0])
		comments := []map[string]any{}
		for _, c := range s.Comments {
			if c.Issue == n {
				comments = append(comments, commentOut(c))
			}
		}
		printJSON(map[string]any{"comments": comments})
	case "issue edit":
		n, _ := strconv.Atoi(rest[0])
		for i := range s.Issues {
			if s.Issues[i].Number != n {
				continue
			}
			for _, l := range flagVals(rest, "--add-label") {
				if !slices.Contains(s.Issues[i].Labels, l) {
					s.Issues[i].Labels = append(s.Issues[i].Labels, l)
				}
			}
			s.Issues[i].Labels = slices.DeleteFunc(s.Issues[i].Labels, func(l string) bool { return slices.Contains(flagVals(rest, "--remove-label"), l) })
		}
		s.Writes++
	case "issue create":
		n := len(s.Issues) + 1
		url := fmt.Sprintf("https://github.com/acme/widget/issues/%d", n)
		s.Issues = append(s.Issues, issue{Number: n, Title: flagVal(rest, "--title"), URL: url, Body: stdin(), Labels: flagVals(rest, "--label"),
			CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		s.Writes++
		fmt.Println(url)
	case "issue comment":
		n, _ := strconv.Atoi(rest[0])
		s.Comments = append(s.Comments, comment{Issue: n, Body: stdin(), Author: cmp.Or(s.User, "foundry-bot"),
			CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		s.Writes++
	default:
		fmt.Fprintln(os.Stderr, "fake gh: unhandled", cmd)
		return 1
	}
	return 0
}

func commentOut(c comment) map[string]any {
	return map[string]any{"author": map[string]any{"login": c.Author}, "body": c.Body, "createdAt": c.CreatedAt}
}

func flagVal(args []string, name string) string {
	if v := flagVals(args, name); len(v) > 0 {
		return v[0]
	}
	return ""
}

func flagVals(args []string, name string) []string {
	var out []string
	for i, a := range args {
		if a == name && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func stdin() string {
	b, _ := io.ReadAll(os.Stdin)
	return string(b)
}

func remoteHead(branch string) string {
	out, _ := exec.Command("git", "ls-remote", "origin", "refs/heads/"+branch).Output()
	if f := strings.Fields(string(out)); len(f) > 0 {
		return f[0]
	}
	return ""
}

func printJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func save(path string, s state) {
	b, _ := json.Marshal(s)
	os.WriteFile(path, b, 0o644)
}
