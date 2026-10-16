package work

import (
	"fmt"
	"strings"

	"github.com/tunedev/foundry/internal/core/ports"
)

// Prompt is the task an unattended agent gets for one friction issue.
func Prompt(is ports.Issue, verify []string) string {
	cmds := make([]string, len(verify))
	for i, v := range verify {
		cmds[i] = "`" + v + "`"
	}
	return fmt.Sprintf(`Fix friction issue #%d in this repository. Nobody will answer questions during this run.

Title: %s

%s

Verify your change with: %s.

Ship with the foundry-ship skill: run `+"`foundry ship`"+` until it prints `+"`ready for human`"+`. Under the recap's Links heading write `+"`Closes #%d`"+`.

Two rules override the skill, because nobody is watching:
- If the fix is a product decision rather than an implementation, change nothing and reply with a final message starting `+"`%s`"+` followed by the options and your recommendation.
- Wherever the skill says to report to or ask the user, stop and reply with a final message starting `+"`%s`"+` followed by the reason.

Reach GitHub only through foundry. Never run gh, git push or vouch ack.
`, is.Number, is.Title, strings.TrimSpace(is.Body), strings.Join(cmds, ", "), is.Number, decisionPrefix, stuckPrefix)
}
