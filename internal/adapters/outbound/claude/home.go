package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tunedev/foundry/skills"
)

// Home is what an agent's HOME holds: the bot's git identity, vouch's hooks, Claude Code's permission
// allowlist and the foundry-ship skill. Secrets are the absolute paths of foundry's secret files.
type Home struct {
	BotName, BotEmail, HooksPath string
	Verify, Secrets              []string
}

// PrepareHome writes the agent's .gitconfig, .claude/settings.json and foundry-ship skill under dir,
// replacing what is there.
func PrepareHome(dir string, h Home) error {
	git := fmt.Sprintf(`[user]
	name = %s
	email = %s
[core]
	hooksPath = %s
[credential "https://github.com"]
	helper =
	helper = !gh auth git-credential
[url "https://github.com/"]
	insteadOf = git@github.com:
`, h.BotName, h.BotEmail, h.HooksPath)
	settings, err := json.MarshalIndent(map[string]any{"permissions": map[string]any{
		"allow": append([]string{"Read", "Glob", "Grep", "Edit(./**)", "Write(./**)", "Bash(go:*)", "Bash(git:*)",
			"Bash(foundry:*)", "Bash(vouch:*)"}, bashRules(h.Verify)...),
		"deny": append([]string{"Bash(gh:*)", "Bash(git push:*)", "Bash(git -c:*)", "Bash(vouch ack:*)", "Bash(vouch rules:*)",
			"WebFetch", "WebSearch"}, readRules(h.Secrets)...),
		"blockReadsOutsideWorkingDirectories": true,
	}}, "", "  ")
	if err != nil {
		return err
	}
	for path, content := range map[string][]byte{
		filepath.Join(dir, ".gitconfig"):                                    []byte(git),
		filepath.Join(dir, ".claude", "settings.json"):                      settings,
		filepath.Join(dir, ".claude", "skills", "foundry-ship", "SKILL.md"): skills.FoundryShip,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// readRules are Read rules for absolute paths, which Claude Code writes with a leading "//".
func readRules(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = "Read(/" + p + ")"
	}
	return out
}

func bashRules(cmds []string) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = "Bash(" + c + ")"
	}
	return out
}
