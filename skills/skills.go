// Package skills embeds the agent skills foundry installs for its agents.
package skills

import _ "embed"

//go:embed foundry-ship/SKILL.md
var FoundryShip []byte
