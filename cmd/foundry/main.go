// Command foundry ships a branch to a green pull request and files the friction met on the way.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/tunedev/foundry/internal/config"
	"github.com/tunedev/foundry/internal/core/app"
	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
)

const usage = `usage: foundry <command>

  ship [--recap <file>] [--title <t>]
        move this branch through preflight, checks, review, push-pr, ci and friction to a green PR
  friction file --title <t> --body-file <f> --kind ready|needs-decision|cleanup
        file a friction report, or comment on a similar open one
  friction none --reason <r>
        record that this branch hit no friction
  config
        print the resolved config
`

const (
	exitOK    = 0
	exitStop  = 1
	exitUsage = 2
)

func main() { os.Exit(dispatch(os.Args[1:])) }

func dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	home, err := config.Home(os.Getenv)
	if err != nil {
		return usageErr(err)
	}
	cfg, err := config.Load(home, os.Getenv)
	if err != nil {
		return usageErr(err)
	}
	logs, err := openLogs(cfg)
	if err != nil {
		return usageErr(err)
	}
	defer logs.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "ship":
		return cmdShip(ctx, cfg, args[1:])
	case "friction":
		return cmdFriction(ctx, cfg, args[1:])
	case "config":
		return cmdConfig(cfg)
	}
	fmt.Fprint(os.Stderr, usage)
	return exitUsage
}

func usageErr(err error) int {
	fmt.Fprintln(os.Stderr, "foundry:", err)
	return exitUsage
}

// openLogs appends structured logs to <home>/logs/foundry.jsonl.
func openLogs(cfg config.Config) (*os.File, error) {
	level, _ := cfg.Level()
	dir := filepath.Join(cfg.Home, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "foundry.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level})))
	return f, nil
}

func cmdShip(ctx context.Context, cfg config.Config, args []string) int {
	return usageErr(errors.New("ship is not built yet"))
}

func cmdFriction(ctx context.Context, cfg config.Config, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	w, err := wireRepo(ctx, cfg)
	if err != nil {
		return usageErr(err)
	}
	branch, err := w.git.Branch(ctx, w.dir)
	if err != nil {
		return usageErr(err)
	}
	switch args[0] {
	case "file":
		return frictionFile(ctx, w, branch, args[1:])
	case "none":
		fs := flag.NewFlagSet("friction none", flag.ContinueOnError)
		reason := fs.String("reason", "", "why this branch hit no friction")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if err := app.NoFriction(w.frictionDeps(), w.id, branch, *reason); err != nil {
			return stopWith(err)
		}
		fmt.Println("recorded: no friction on", branch)
		return exitOK
	}
	fmt.Fprint(os.Stderr, usage)
	return exitUsage
}

func frictionFile(ctx context.Context, w repoEnv, branch string, args []string) int {
	fs := flag.NewFlagSet("friction file", flag.ContinueOnError)
	title := fs.String("title", "", "issue title naming the symptom")
	bodyFile := fs.String("body-file", "", "file holding the report body")
	kindFlag := fs.String("kind", "", "ready, needs-decision or cleanup")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	kind, err := friction.ParseKind(*kindFlag)
	if err != nil {
		return usageErr(err)
	}
	if *title == "" || *bodyFile == "" {
		return usageErr(errors.New("--title and --body-file are required"))
	}
	body, err := os.ReadFile(*bodyFile)
	if err != nil {
		return usageErr(err)
	}
	f, err := app.FileFriction(ctx, w.frictionDeps(), app.FileInput{
		Dir: w.dir, Repo: w.id, Branch: branch, Title: *title, Body: string(body), Kind: kind,
		UserDataPaths: w.cfg.UserDataPaths, Max: w.repo.MaxFrictionPerShip, RetryPause: w.cfg.RetryPause.Duration,
	})
	if err != nil {
		return stopWith(err)
	}
	if f.Duplicate {
		fmt.Printf("commented on #%d (similar open issue): %s\n", f.Issue.Number, f.Issue.URL)
	} else {
		fmt.Printf("filed #%d: %s\n", f.Issue.Number, f.Issue.URL)
	}
	return exitOK
}

func cmdConfig(cfg config.Config) int {
	fmt.Printf("# home = %s\n", cfg.Home)
	if err := toml.NewEncoder(os.Stdout).Encode(cfg); err != nil {
		return usageErr(err)
	}
	return exitOK
}

// stopWith prints why a command stopped, as the last line an agent reads, and returns exitStop.
func stopWith(err error) int {
	var na *app.NeedsAction
	switch {
	case errors.As(err, &na):
		fmt.Printf("stop at %s: %s\n", na.Step, na.Detail)
	case errors.Is(err, ports.ErrTransient):
		fmt.Printf("stop: transient failure survived one retry; rerun later: %v\n", err)
	default:
		fmt.Printf("stop: %v\n", err)
	}
	return exitStop
}

func outcome(err error) string {
	var na *app.NeedsAction
	switch {
	case err == nil:
		return "ready-for-human"
	case errors.As(err, &na):
		return "needs-action"
	case errors.Is(err, ports.ErrTransient):
		return "transient"
	}
	return "error"
}
