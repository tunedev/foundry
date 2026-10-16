package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadLayersDefaultsFileThenEnv(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("ci_poll = \"5s\"\ncall_timeout = \"1m\"\n"), 0o644)
	c, err := Load(home, env(map[string]string{"FOUNDRY_CI_POLL": "1s"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.CIPoll.Duration != time.Second || c.CallTimeout.Duration != time.Minute || c.ReviewTimeout.Duration != 30*time.Minute {
		t.Fatalf("ci_poll=%s call_timeout=%s review_timeout=%s", c.CIPoll, c.CallTimeout, c.ReviewTimeout)
	}
}

func TestLoadFailsLoudlyOnBadConfig(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("ci_pol = \"5s\"\n"), 0o644)
	if _, err := Load(home, env(nil)); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Errorf("unknown key: %v", err)
	}
	clean := t.TempDir()
	if _, err := Load(clean, env(map[string]string{"FOUNDRY_CI_POLL": "0s"})); err == nil || !strings.Contains(err.Error(), "ci_poll must be positive") {
		t.Errorf("zero duration: %v", err)
	}
	if _, err := Load(clean, env(map[string]string{"FOUNDRY_LOG_LEVEL": "loud"})); err == nil || !strings.Contains(err.Error(), "log_level") {
		t.Errorf("bad level: %v", err)
	}
}

func TestLoadRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadRepo(dir); err == nil || !strings.Contains(err.Error(), "no foundry.toml") {
		t.Errorf("missing file: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "foundry.toml"), []byte("base = \"main\"\n"), 0o644)
	if _, err := LoadRepo(dir); err == nil || !strings.Contains(err.Error(), "checks") {
		t.Errorf("no checks: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "foundry.toml"), []byte("checks = [\"go test ./...\"]\n"), 0o644)
	r, err := LoadRepo(dir)
	if err != nil || r.Base != "main" || r.MaxFrictionPerShip != 3 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestLoadIntegersFromEnv(t *testing.T) {
	c, err := Load(t.TempDir(), env(map[string]string{"FOUNDRY_MAX_REVIEW_ROUNDS": "5"}))
	if err != nil || c.MaxReviewRounds != 5 {
		t.Fatalf("%d %v", c.MaxReviewRounds, err)
	}
	if _, err := Load(t.TempDir(), env(map[string]string{"FOUNDRY_MAX_REVIEW_ROUNDS": "0"})); err == nil || !strings.Contains(err.Error(), "max_review_rounds must be at least 1") {
		t.Errorf("zero rounds: %v", err)
	}
}

func TestLoadWorkDefaultsAndSecretPaths(t *testing.T) {
	home := t.TempDir()
	c, err := Load(home, env(map[string]string{"FOUNDRY_MAX_ISSUES_PER_RUN": "1", "FOUNDRY_CLAIM_LEASE": "30m"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxIssuesPerRun != 1 || c.ClaimLease.Duration != 30*time.Minute || c.IssueTimeout.Duration != 45*time.Minute ||
		c.MaxTurns != 150 || c.GitHubTokenFile != filepath.Join(home, "secrets", "github-token") ||
		c.DiscordWebhookFile != filepath.Join(home, "secrets", "discord-webhook") {
		t.Fatalf("%+v", c)
	}
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("work_window = \"25:00-07:00\"\n"), 0o644)
	if _, err := Load(home, env(nil)); err == nil || !strings.Contains(err.Error(), "work_window") {
		t.Errorf("bad window: %v", err)
	}
}

func TestLoadWorkStringAndListKeysFromEnv(t *testing.T) {
	c, err := Load(t.TempDir(), env(map[string]string{
		"FOUNDRY_BOT_NAME":       "foundry-bot",
		"FOUNDRY_WORK_WINDOW":    "22:00-07:00",
		"FOUNDRY_AGENT_PASS_ENV": "A,,B",
	}))
	if err != nil || c.BotName != "foundry-bot" || c.WorkWindow != "22:00-07:00" || strings.Join(c.AgentPassEnv, "|") != "A|B" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Load(t.TempDir(), env(map[string]string{"FOUNDRY_WORK_WINDOW": "25:00-07:00"})); err == nil || !strings.Contains(err.Error(), "work_window") {
		t.Errorf("bad env window: %v", err)
	}
}
