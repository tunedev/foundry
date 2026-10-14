package run

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/tunedev/foundry/internal/core/ports"
)

var transient = regexp.MustCompile(`(?i)HTTP 5\d\d|rate limit|could not resolve host|connection (reset|refused)|i/o timeout|TLS handshake|unexpected EOF`)

// Classify wraps err with ports.ErrTransient when it is a timeout, a network failure, or a forge-side
// failure worth one retry. Every other error is returned unchanged.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTimeout) || transient.MatchString(err.Error()) {
		return fmt.Errorf("%w: %w", ports.ErrTransient, err)
	}
	return err
}
