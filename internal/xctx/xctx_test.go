package xctx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/xctx"
)

func TestTimeoutCause(t *testing.T) {
	t.Parallel()
	ctx, cancel := xctx.WithTimeout(t.Context(), time.Millisecond, "work")
	defer cancel(nil)
	<-ctx.Done()
	err := context.Cause(ctx)
	if want := "work: 1ms timeout: context deadline exceeded"; err == nil || err.Error() != want {
		t.Errorf("Cause = %v, want %q", err, want)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Cause = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

func TestCancelCause(t *testing.T) {
	t.Parallel()
	errStop := errors.New("the user closed the list")
	tests := []struct {
		name string
		with func(context.Context) (context.Context, context.CancelCauseFunc)
	}{
		{"WithCancel", func(ctx context.Context) (context.Context, context.CancelCauseFunc) {
			return xctx.WithCancel(ctx, "work")
		}},
		{"WithTimeout", func(ctx context.Context) (context.Context, context.CancelCauseFunc) {
			return xctx.WithTimeout(ctx, time.Hour, "work")
		}},
	}
	for _, tt := range tests {
		ctx, cancel := tt.with(t.Context())
		child, done := xctx.WithTimeout(ctx, time.Hour, "child")
		cancel(errStop)
		for _, c := range []context.Context{ctx, child} {
			err := context.Cause(c)
			want := "work: the user closed the list: context canceled"
			if !errors.Is(err, errStop) || !errors.Is(err, context.Canceled) || err.Error() != want {
				t.Errorf("%s: Cause = %v, want %q", tt.name, err, want)
			}
			if got := xctx.Err(c, errors.New("exit status 1")); !errors.Is(got, errStop) {
				t.Errorf("%s: Err = %v, want the cause", tt.name, got)
			}
		}
		done(nil)
	}
}

func TestDoneCause(t *testing.T) {
	t.Parallel()
	ctx, cancel := xctx.WithTimeout(t.Context(), time.Hour, "work")
	errRun := errors.New("exit status 1")
	if got := xctx.Err(ctx, errRun); !errors.Is(got, errRun) {
		t.Errorf("Err before the end = %v, want %v", got, errRun)
	}
	cancel(nil)
	err := context.Cause(ctx)
	if want := "work: done: context canceled"; !errors.Is(err, context.Canceled) || err.Error() != want {
		t.Errorf("Cause after cancel(nil) = %v, want %q", err, want)
	}
}
