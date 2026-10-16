package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/tunedev/foundry/internal/adapters/outbound/claude"
	"github.com/tunedev/foundry/internal/adapters/outbound/discord"
	"github.com/tunedev/foundry/internal/adapters/outbound/github"
	"github.com/tunedev/foundry/internal/adapters/outbound/gitx"
	"github.com/tunedev/foundry/internal/adapters/outbound/store"
	"github.com/tunedev/foundry/internal/adapters/outbound/vouch"
	"github.com/tunedev/foundry/internal/config"
	"github.com/tunedev/foundry/internal/core/app"
	"github.com/tunedev/foundry/internal/core/work"
	runner "github.com/tunedev/foundry/internal/run"
)

// repoEnv is the opted-in repo foundry runs in, with the adapters every command uses.
type repoEnv struct {
	cfg     config.Config
	dir, id string
	repo    config.Repo
	git     gitx.Git
	forge   github.GitHub
	store   store.Store
}

// wireRepo resolves the repo containing the working directory and loads its foundry.toml.
func wireRepo(ctx context.Context, cfg config.Config) (repoEnv, error) {
	w := repoEnv{
		cfg:   cfg,
		git:   gitx.Git{Timeout: cfg.CallTimeout.Duration},
		forge: github.GitHub{Timeout: cfg.CallTimeout.Duration},
		store: store.Store{Home: cfg.Home},
	}
	cwd, err := os.Getwd()
	if err != nil {
		return w, err
	}
	if w.dir, err = w.git.Toplevel(ctx, cwd); err != nil {
		return w, err
	}
	if w.repo, err = config.LoadRepo(w.dir); err != nil {
		return w, err
	}
	w.id, err = w.git.RepoID(ctx, w.dir)
	return w, err
}

func (w repoEnv) frictionDeps() app.FrictionDeps {
	return app.FrictionDeps{Forge: w.forge, Load: w.store.Answer, Save: w.store.SaveAnswer}
}

func (w repoEnv) shipDeps() app.ShipDeps {
	v := vouch.Vouch{ReviewTimeout: w.cfg.ReviewTimeout.Duration, CallTimeout: w.cfg.CallTimeout.Duration}
	return app.ShipDeps{
		Forge: w.forge, Branch: w.git.Branch, Clean: w.git.Clean, Ahead: w.git.Ahead, HeadSHA: w.git.HeadSHA,
		RemoteSHA: w.git.RemoteSHA, Push: w.git.Push, Subject: w.git.Subject, Review: v.Review, Answer: w.store.Answer,
		Rounds: w.store.Rounds, SaveRounds: w.store.SaveRounds,
		RunCheck: func(ctx context.Context, dir, command string) error {
			_, err := runner.Cmd(ctx, w.cfg.CheckTimeout.Duration, dir, nil, "sh", "-c", command)
			return err
		},
	}
}

// workDeps runs the agent as claude with exactly env, and sends summaries to the Discord webhook.
func (w repoEnv) workDeps(sec secrets, env []string) app.WorkDeps {
	return app.WorkDeps{
		Forge:    w.forge,
		Agent:    claude.Runner{Bin: "claude", WorktreeRoot: filepath.Join(w.cfg.Home, "worktrees"), GitTimeout: w.cfg.CallTimeout.Duration, Env: env},
		Notifier: discord.Webhook{URL: sec.discordWebhook, Timeout: w.cfg.CallTimeout.Duration},
		LastShip: func(repo, branch string) (work.ShipRecord, bool, error) {
			r, ok, err := w.store.LastRun(repo, branch)
			return work.ShipRecord{Step: r.Step, Outcome: r.Outcome, PRURL: r.PRURL, Detail: r.Detail}, ok, err
		},
		Now: time.Now,
	}
}
