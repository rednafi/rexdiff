// Package xctx has helpers for contexts.
package xctx

import (
	"context"
	"fmt"
	"time"
)

// WithTimeout returns a copy of parent that is cancelled after d. Its cause
// is then "what: d timeout", which wraps [context.DeadlineExceeded]. Call
// the returned function when the work is done.
func WithTimeout(parent context.Context, d time.Duration, what string) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(parent, d, fmt.Errorf("%s: %v timeout: %w", what, d, context.DeadlineExceeded))
}
