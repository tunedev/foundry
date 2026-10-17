// Command fakeclaude stands in for claude in foundry's end-to-end tests. It reads the prompt on stdin, takes
// the issue number from "Closes #<n>", records its environment and prompt in $FAKE_LOGS/claude-<n>.env and
// .prompt, and acts out the scenario in $FAKE_LOGS/claude-<n>.scenario (default ship): ship, needs-decision,
// stuck, hang, crash or review-loop. It runs foundry with its own environment, so TRACEPARENT passes through.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var logs = os.Getenv("FAKE_LOGS")

func main() {
	prompt, _ := io.ReadAll(os.Stdin)
	m := regexp.MustCompile(`Closes #(\d+)`).FindSubmatch(prompt)
	if m == nil {
		fmt.Fprintln(os.Stderr, "fake claude: no issue in prompt")
		os.Exit(2)
	}
	n := string(m[1])
	write("claude-"+n+".env", strings.Join(os.Environ(), "\n"))
	write("claude-"+n+".prompt", string(prompt))
	scenario, _ := os.ReadFile(filepath.Join(logs, "claude-"+n+".scenario"))
	switch strings.TrimSpace(string(scenario)) {
	case "", "ship":
		commit("fix #" + n)
		foundry("friction", "none", "--reason", "smooth run")
		result(lastLine(foundry("ship", "--recap", recap(n))))
	case "needs-decision":
		result("NEEDS-DECISION: rename the flag or keep it; I recommend keeping it.")
	case "stuck":
		result("STUCK: vouch failed to run")
	case "hang":
		child := exec.Command("sleep", "300")
		child.Start()
		write("claude-"+n+".child", fmt.Sprint(child.Process.Pid))
		time.Sleep(time.Hour)
	case "crash":
		fmt.Fprintln(os.Stderr, "panic: fake crash")
		os.Exit(3)
	case "review-loop":
		write("vouch-gate-fail", "- minor calc.go:1: unbounded retry\n")
		for i := range 5 {
			commit(fmt.Sprint("attempt ", i))
			if strings.Contains(foundry("ship", "--recap", recap(n)), "did not converge") {
				break
			}
		}
		result("STUCK: review did not converge")
	}
}

func write(name, content string) { os.WriteFile(filepath.Join(logs, name), []byte(content), 0o644) }

func commit(msg string) {
	f, _ := os.OpenFile("calc.go", os.O_APPEND|os.O_WRONLY, 0o644)
	fmt.Fprintf(f, "// %s\n", msg)
	f.Close()
	if out, err := exec.Command("git", "commit", "-qam", msg).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "fake claude: git commit: %v\n%s", err, out)
		os.Exit(4)
	}
}

func foundry(args ...string) string {
	out, _ := exec.Command("foundry", args...).Output()
	return string(out)
}

func recap(n string) string {
	p := filepath.Join(os.TempDir(), "fake-recap-"+n+".md")
	os.WriteFile(p, []byte("## What and why\nfix\n## Touched\ncalc\n## Risk\nlow: one line\n## Verified\ntrue\n## Links\nCloses #"+n+"\n"), 0o644)
	return p
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func result(text string) {
	b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text,
		"session_id": "fake-session", "num_turns": 3, "total_cost_usd": 0.25, "modelUsage": map[string]any{"fake-model": map[string]any{}}})
	fmt.Println(string(b))
}
