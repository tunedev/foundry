// Package store keeps foundry's local state under its home directory.
package store

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ship"
)

type Store struct{ Home string }

func (s Store) answerPath(repo, branch string) string {
	return filepath.Join(s.Home, "repos", filepath.FromSlash(repo), "friction", url.PathEscape(branch)+".json")
}

// Answer returns what branch reported at the friction step; a branch that reported nothing has a zero Answer.
func (s Store) Answer(repo, branch string) (friction.Answer, error) {
	var a friction.Answer
	b, err := os.ReadFile(s.answerPath(repo, branch))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	return a, json.Unmarshal(b, &a)
}

func (s Store) SaveAnswer(repo, branch string, a friction.Answer) error {
	return writeJSON(s.answerPath(repo, branch), a)
}

// Run is one line of runs.jsonl.
type Run struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Repo    string    `json:"repo"`
	Branch  string    `json:"branch"`
	Step    string    `json:"step"`
	Outcome string    `json:"outcome"`
	PRURL   string    `json:"pr_url,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	WallMS  int64     `json:"wall_ms"`
}

func (s Store) AppendRun(r Run) error {
	if err := os.MkdirAll(s.Home, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Home, "runs.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s Store) roundsPath(repo, branch string) string {
	return filepath.Join(s.Home, "repos", filepath.FromSlash(repo), "review", url.PathEscape(branch)+".json")
}

type roundsJSON struct {
	Count   int    `json:"count"`
	LastSHA string `json:"last_sha"`
}

// Rounds returns the review rounds that ended in findings on branch, and the commit the last one reviewed.
func (s Store) Rounds(repo, branch string) (ship.Rounds, error) {
	var r roundsJSON
	b, err := os.ReadFile(s.roundsPath(repo, branch))
	if errors.Is(err, os.ErrNotExist) {
		return ship.Rounds{}, nil
	}
	if err != nil {
		return ship.Rounds{}, err
	}
	err = json.Unmarshal(b, &r)
	return ship.Rounds{Count: r.Count, LastSHA: r.LastSHA}, err
}

func (s Store) SaveRounds(repo, branch string, r ship.Rounds) error {
	return writeJSON(s.roundsPath(repo, branch), roundsJSON{Count: r.Count, LastSHA: r.LastSHA})
}

// LastRun returns the newest ship record for repo and branch.
func (s Store) LastRun(repo, branch string) (Run, bool, error) {
	b, err := os.ReadFile(filepath.Join(s.Home, "runs.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	var last Run
	found := false
	for line := range strings.Lines(string(b)) {
		var r Run
		if json.Unmarshal([]byte(line), &r) == nil && r.Kind == "ship" && r.Repo == repo && r.Branch == branch {
			last, found = r, true
		}
	}
	return last, found, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
