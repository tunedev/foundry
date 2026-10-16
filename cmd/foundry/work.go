package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tunedev/foundry/internal/adapters/outbound/claude"
	"github.com/tunedev/foundry/internal/config"
	"github.com/tunedev/foundry/internal/core/app"
)

type secrets struct{ githubToken, claudeToken, discordWebhook string }

func cmdWork(ctx context.Context, cfg config.Config, args []string) int {
	if len(args) > 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	unlock, held, err := lockWork(filepath.Join(cfg.Home, "work.lock"))
	if err != nil {
		return usageErr(err)
	}
	if held {
		slog.InfoContext(ctx, "work skipped: another run holds the lock")
		fmt.Println("skip: another foundry work run holds the lock")
		return exitOK
	}
	defer unlock()
	if cfg.BotName == "" || cfg.BotEmail == "" {
		return usageErr(errors.New("work: set bot_name and bot_email in config.toml"))
	}
	sec, err := loadSecrets(cfg)
	if err != nil {
		return usageErr(err)
	}
	w, err := wireRepo(ctx, cfg)
	if err != nil {
		return usageErr(err)
	}
	w.forge.Token = sec.githubToken
	if err := checkBotLogin(ctx, w, cfg.BotName); err != nil {
		return usageErr(err)
	}
	vouchHome := os.Getenv("VOUCH_HOME")
	if vouchHome == "" {
		u, _ := os.UserHomeDir()
		vouchHome = filepath.Join(u, ".vouch")
	}
	agentHome := filepath.Join(cfg.Home, "agent")
	home := claude.Home{BotName: cfg.BotName, BotEmail: cfg.BotEmail, HooksPath: filepath.Join(vouchHome, "githooks"),
		Verify: w.repo.Verify, Secrets: []string{cfg.GitHubTokenFile, cfg.ClaudeTokenFile, cfg.DiscordWebhookFile}}
	if err := claude.PrepareHome(agentHome, home); err != nil {
		return usageErr(err)
	}
	window, _ := cfg.Window()
	s, err := app.Work(ctx, w.workDeps(sec, agentEnv(cfg, sec, agentHome, vouchHome, os.Getenv)), app.WorkInput{
		Dir: w.dir, Repo: w.id, Base: w.repo.Base, RunID: time.Now().UTC().Format("20060102-150405"), Bot: cfg.BotName,
		Trusted: []string{cfg.BotName, owner(w.id)},
		Verify:  w.repo.Verify, UserDataPaths: cfg.UserDataPaths, Window: window, ClaimLease: cfg.ClaimLease.Duration,
		IssueTimeout: cfg.IssueTimeout.Duration, RetryPause: cfg.RetryPause.Duration, MaxIssues: cfg.MaxIssuesPerRun, MaxTurns: cfg.MaxTurns,
	})
	for _, is := range s.Issues {
		fmt.Printf("#%d %s %s %s\n", is.Number, is.Outcome, is.Reason, is.PRURL)
	}
	if err != nil {
		return stopWith(err)
	}
	return exitOK
}

// checkBotLogin fails unless the bot token acts as bot_name, the login claims and trust are compared against.
func checkBotLogin(ctx context.Context, w repoEnv, bot string) error {
	login, err := w.forge.Login(ctx, w.dir)
	if err != nil {
		return fmt.Errorf("work: reading the bot token's GitHub login: %w", err)
	}
	if !strings.EqualFold(login, bot) {
		return fmt.Errorf("work: bot_name is %q but the bot token acts as GitHub login %q; set bot_name to the bot's login", bot, login)
	}
	return nil
}

// owner is the account that owns repo id owner/name.
func owner(id string) string {
	o, _, _ := strings.Cut(id, "/")
	return o
}

// lockWork takes an exclusive, non-blocking flock on path; held reports that another process has it. The
// kernel drops the lock when its holder exits, however it exits.
func lockWork(path string) (unlock func(), held bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() { f.Close() }, false, nil
}

// loadSecrets reads the three secret files in a fixed order and stops at the first unreadable or empty one.
// Errors name the file, never its content.
func loadSecrets(cfg config.Config) (secrets, error) {
	var s secrets
	for _, f := range []struct {
		path string
		dst  *string
	}{{cfg.GitHubTokenFile, &s.githubToken}, {cfg.ClaudeTokenFile, &s.claudeToken}, {cfg.DiscordWebhookFile, &s.discordWebhook}} {
		b, err := os.ReadFile(f.path)
		if err != nil {
			return s, fmt.Errorf("secret file %s: %w", f.path, errors.Unwrap(err))
		}
		if *f.dst = strings.TrimSpace(string(b)); *f.dst == "" {
			return s, fmt.Errorf("secret file %s is empty", f.path)
		}
	}
	return s, nil
}

// agentEnv is the agent's whole environment, apart from the trace context the runner adds.
func agentEnv(cfg config.Config, sec secrets, home, vouchHome string, getenv func(string) string) []string {
	or := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	env := []string{
		"HOME=" + home, "PATH=" + getenv("PATH"), "GH_TOKEN=" + sec.githubToken, "CLAUDE_CODE_OAUTH_TOKEN=" + sec.claudeToken,
		"VOUCH_HOME=" + vouchHome, "FOUNDRY_HOME=" + cfg.Home,
		"CLAUDE_CODE_ENABLE_TELEMETRY=1", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1",
		"OTEL_TRACES_EXPORTER=" + or("OTEL_TRACES_EXPORTER", "otlp"), "OTEL_METRICS_EXPORTER=" + or("OTEL_METRICS_EXPORTER", "otlp"),
		"OTEL_LOGS_EXPORTER=none", "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + or("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318"),
	}
	for _, name := range cfg.AgentPassEnv {
		if v := getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}
