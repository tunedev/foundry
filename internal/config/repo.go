package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Repo is foundry.toml at a repo's root.
type Repo struct {
	Base               string   `toml:"base"`
	Checks             []string `toml:"checks"`
	Verify             []string `toml:"verify"`
	MaxFrictionPerShip int      `toml:"max_friction_per_ship"`
}

// LoadRepo reads and validates foundry.toml in dir. A directory without one is not opted in.
func LoadRepo(dir string) (Repo, error) {
	r := Repo{Base: "main", MaxFrictionPerShip: 3}
	md, err := toml.DecodeFile(filepath.Join(dir, "foundry.toml"), &r)
	if errors.Is(err, os.ErrNotExist) {
		return r, fmt.Errorf("%s has no foundry.toml; add one to opt the repo in", dir)
	}
	if err != nil {
		return r, fmt.Errorf("foundry.toml: %w", err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return r, fmt.Errorf("foundry.toml: unknown keys %v", u)
	}
	var errs []error
	if r.Base == "" {
		errs = append(errs, errors.New("foundry.toml: base must not be empty"))
	}
	if len(r.Checks) == 0 {
		errs = append(errs, errors.New("foundry.toml: checks must list at least one command"))
	}
	if r.MaxFrictionPerShip < 1 {
		errs = append(errs, errors.New("foundry.toml: max_friction_per_ship must be at least 1"))
	}
	return r, errors.Join(errs...)
}
