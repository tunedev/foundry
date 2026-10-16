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
	if v := getenv("FOUNDRY_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	return c, c.validate()
}

func (c *Config) durations() map[string]*Duration {
	return map[string]*Duration{
		"call_timeout": &c.CallTimeout, "review_timeout": &c.ReviewTimeout, "check_timeout": &c.CheckTimeout,
		"ci_timeout": &c.CITimeout, "ci_poll": &c.CIPoll, "retry_pause": &c.RetryPause,
	}
}

func (c *Config) ints() map[string]*int {
	return map[string]*int{"max_review_rounds": &c.MaxReviewRounds}
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
