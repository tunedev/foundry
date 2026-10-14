// Package store keeps foundry's local state under its home directory.
package store

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/tunedev/foundry/internal/core/friction"
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
	p := s.answerPath(repo, branch)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
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
