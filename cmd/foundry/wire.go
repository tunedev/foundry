package main

import (
	"context"
	"os"

	"github.com/tunedev/foundry/internal/adapters/outbound/github"
	"github.com/tunedev/foundry/internal/adapters/outbound/gitx"
	"github.com/tunedev/foundry/internal/adapters/outbound/store"
	"github.com/tunedev/foundry/internal/config"
	"github.com/tunedev/foundry/internal/core/app"
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
