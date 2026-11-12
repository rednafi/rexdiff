// Package xctx makes contexts whose cancellation has a cause. Each cause
// wraps [context.Canceled] or [context.DeadlineExceeded], so [errors.Is]
// still matches.
package xctx

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// WithCancel is like [context.WithCancelCause], but the cause wraps
// [context.Canceled] and names what was canceled. A nil cause means that
// the work is done. The cause is then "<what>: done".
func WithCancel(parent context.Context, what string) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(parent) //nolint:forbidigo // xctx wraps it
	return ctx, func(cause error) {
		switch {
		case cause == nil:
			cause = fmt.Errorf("%s: done: %w", what, context.Canceled)
		case !errors.Is(cause, context.Canceled):
			cause = fmt.Errorf("%s: %w: %w", what, cause, context.Canceled)
		}
		cancel(cause)
	}
}

// WithTimeout is like [WithCancel], but ctx also ends after d. The cause
// then names what timed out.
func WithTimeout(parent context.Context, d time.Duration, what string) (context.Context, context.CancelCauseFunc) {
	// The cancel of a timeout takes no cause, so stack one that does.
	ctx, cancel := WithCancel(parent, what)
	ctx, stop := context.WithTimeoutCause(ctx, d, //nolint:forbidigo // xctx wraps it
		fmt.Errorf("%s: %v timeout: %w", what, d, context.DeadlineExceeded))
	return ctx, func(cause error) {
		cancel(cause)
		stop()
	}
}

// Err returns the cause of ctx if ctx is done, and err if not. Use it for
// an error from work that ctx can stop.
func Err(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}
