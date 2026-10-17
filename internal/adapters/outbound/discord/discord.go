// Package discord sends run summaries to a Discord channel webhook.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

// Webhook posts to URL, which is a secret: no error it returns quotes it.
type Webhook struct {
	URL     string
	Timeout time.Duration
}

// Notify posts the rendered summary with mentions disabled. A network failure, 429 or 5xx is ErrTransient.
func (w Webhook) Notify(ctx context.Context, s ports.Summary) error {
	body, err := json.Marshal(map[string]any{"content": Render(s), "allowed_mentions": map[string]any{"parse": []string{}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("discord webhook: invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: w.Timeout}).Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return fmt.Errorf("%w: discord webhook: %v", ports.ErrTransient, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("%w: discord webhook: HTTP %d", ports.ErrTransient, resp.StatusCode)
	}
	return fmt.Errorf("discord webhook: HTTP %d", resp.StatusCode)
}

// Render writes the summary as a Discord message of at most 2000 characters. The abort reason comes
// first, then the issues that need a person, then the ready ones; titles are cut at titleMax characters,
// and a message over the limit is cut and ends with "(truncated)".
func Render(s ports.Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "foundry run %s on %s: %d issues, $%.2f, %s\n", s.RunID, s.Repo, len(s.Issues), s.CostUSD, s.Wall.Round(time.Second))
	if s.Aborted != "" {
		b.WriteString("Ended early: " + s.Aborted + "\n")
	}
	section := func(title, outcome string, line func(ports.IssueReport) string) {
		var lines []string
		for _, is := range s.Issues {
			if is.Outcome == outcome {
				is.Title = cut(is.Title, titleMax)
				lines = append(lines, "- "+line(is))
			}
		}
		if len(lines) > 0 {
			b.WriteString(title + ":\n" + strings.Join(lines, "\n") + "\n")
		}
	}
	section("Stuck", "stuck", func(is ports.IssueReport) string { return fmt.Sprintf("#%d %s: %s", is.Number, is.Title, is.Reason) })
	section("Needs a decision", "needs-decision", func(is ports.IssueReport) string { return fmt.Sprintf("#%d %s", is.Number, is.Title) })
	section("Ready for review", "ready-for-human", func(is ports.IssueReport) string { return fmt.Sprintf("#%d %s: %s", is.Number, is.Title, is.PRURL) })
	return truncate(b.String(), 2000)
}

const titleMax = 120

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// truncate cuts s to at most n characters, ending a cut message with a marker.
func truncate(s string, n int) string {
	const marker = "\n(truncated)"
	if len([]rune(s)) <= n {
		return s
	}
	return cut(s, n-len(marker)) + marker
}
