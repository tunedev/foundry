// Package config resolves foundry configuration: defaults, then config.toml, then FOUNDRY_* env.
// A repo's own settings come from foundry.toml at its root.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/tunedev/foundry/internal/core/work"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	CallTimeout     Duration `toml:"call_timeout"`
	ReviewTimeout   Duration `toml:"review_timeout"`
	CheckTimeout    Duration `toml:"check_timeout"`
	CITimeout       Duration `toml:"ci_timeout"`
	CIPoll          Duration `toml:"ci_poll"`
	RetryPause      Duration `toml:"retry_pause"`
	MaxReviewRounds int      `toml:"max_review_rounds"`
	UserDataPaths   []string `toml:"user_data_paths"`
	LogLevel        string   `toml:"log_level"`
	Home            string   `toml:"-"`

	WorkWindow         string   `toml:"work_window"`
	ClaimLease         Duration `toml:"claim_lease"`
	IssueTimeout       Duration `toml:"issue_timeout"`
	MaxIssuesPerRun    int      `toml:"max_issues_per_run"`
	MaxTurns           int      `toml:"max_turns"`
	GitHubTokenFile    string   `toml:"github_token_file"`
	ClaudeTokenFile    string   `toml:"claude_token_file"`
	DiscordWebhookFile string   `toml:"discord_webhook_file"`
	BotName            string   `toml:"bot_name"`
	BotEmail           string   `toml:"bot_email"`
	AgentPassEnv       []string `toml:"agent_pass_env"`
}

func Defaults() Config {
	return Config{
		CallTimeout:     Duration{2 * time.Minute},
		ReviewTimeout:   Duration{30 * time.Minute},
		CheckTimeout:    Duration{30 * time.Minute},
		CITimeout:       Duration{45 * time.Minute},
		CIPoll:          Duration{30 * time.Second},
		RetryPause:      Duration{2 * time.Second},
		MaxReviewRounds: 3,
		LogLevel:        "info",
		ClaimLease:      Duration{2 * time.Hour},
		IssueTimeout:    Duration{45 * time.Minute},
		MaxIssuesPerRun: 3,
		MaxTurns:        150,
	}
}

// Home returns FOUNDRY_HOME, or ~/.foundry.
func Home(getenv func(string) string) (string, error) {
	if h := getenv("FOUNDRY_HOME"); h != "" {
		return h, nil
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(u, ".foundry"), nil
}

// Load resolves defaults, then <home>/config.toml, then FOUNDRY_<KEY> env, and validates the result.
func Load(home string, getenv func(string) string) (Config, error) {
	c := Defaults()
	c.Home = home
	md, err := toml.DecodeFile(filepath.Join(home, "config.toml"), &c)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, fmt.Errorf("config.toml: %w", err)
	case len(md.Undecoded()) > 0:
		return c, fmt.Errorf("config.toml: unknown keys %v", md.Undecoded())
	}
	for key, d := range c.durations() {
		name := "FOUNDRY_" + strings.ToUpper(key)
		if v := getenv(name); v != "" {
			if err := d.UnmarshalText([]byte(v)); err != nil {
				return c, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	for key, n := range c.ints() {
		name := "FOUNDRY_" + strings.ToUpper(key)
		if v := getenv(name); v != "" {
			i, err := strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("%s: %w", name, err)
			}
			*n = i
		}
	}
	for key, p := range c.strings() {
		if v := getenv("FOUNDRY_" + strings.ToUpper(key)); v != "" {
			*p = v
		}
	}
	if v := getenv("FOUNDRY_AGENT_PASS_ENV"); v != "" {
		c.AgentPassEnv = nonEmpty(strings.Split(v, ","))
	}
	for p, name := range map[*string]string{&c.GitHubTokenFile: "github-token", &c.ClaudeTokenFile: "claude-oauth-token", &c.DiscordWebhookFile: "discord-webhook"} {
		if *p == "" {
			*p = filepath.Join(home, "secrets", name)
		}
	}
	return c, c.validate()
}

func nonEmpty(items []string) []string {
	var out []string
	for _, s := range items {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (c *Config) strings() map[string]*string {
	return map[string]*string{
		"log_level": &c.LogLevel, "work_window": &c.WorkWindow, "bot_name": &c.BotName, "bot_email": &c.BotEmail,
		"github_token_file": &c.GitHubTokenFile, "claude_token_file": &c.ClaudeTokenFile, "discord_webhook_file": &c.DiscordWebhookFile,
	}
}

func (c *Config) durations() map[string]*Duration {
	return map[string]*Duration{
		"call_timeout": &c.CallTimeout, "review_timeout": &c.ReviewTimeout, "check_timeout": &c.CheckTimeout,
		"ci_timeout": &c.CITimeout, "ci_poll": &c.CIPoll, "retry_pause": &c.RetryPause,
		"claim_lease": &c.ClaimLease, "issue_timeout": &c.IssueTimeout,
	}
}

func (c *Config) ints() map[string]*int {
	return map[string]*int{
		"max_review_rounds": &c.MaxReviewRounds, "max_issues_per_run": &c.MaxIssuesPerRun, "max_turns": &c.MaxTurns,
	}
}

func (c *Config) validate() error {
	var errs []error
	for key, d := range c.durations() {
		if d.Duration <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", key))
		}
	}
	for key, n := range c.ints() {
		if *n < 1 {
			errs = append(errs, fmt.Errorf("%s must be at least 1", key))
		}
	}
	if _, err := c.Window(); err != nil {
		errs = append(errs, err)
	}
	if _, err := c.Level(); err != nil {
		errs = append(errs, fmt.Errorf("log_level: %w", err))
	}
	return errors.Join(errs...)
}

func (c Config) Level() (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(c.LogLevel))
	return l, err
}

// Window is the daily stretch of local time in which work starts issues.
func (c Config) Window() (work.Window, error) { return work.ParseWindow(c.WorkWindow) }
