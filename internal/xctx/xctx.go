// Package xctx has helpers for contexts.
package xctx

import (
	"context"
	"fmt"
	"time"
)

// WithTimeout is like [context.WithTimeout], but the cause names what timed
// out.
func WithTimeout(parent context.Context, d time.Duration, what string) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(parent, d, fmt.Errorf("%s: %v timeout: %w", what, d, context.DeadlineExceeded))
}
