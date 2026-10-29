package xctx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/xctx"
)

func TestContextTimesOut(t *testing.T) {
	t.Parallel()
	ctx, cancel := xctx.WithTimeout(t.Context(), time.Millisecond, "work")
	defer cancel()
	<-ctx.Done()
	err := context.Cause(ctx)
	if want := "work: 1ms timeout: context deadline exceeded"; err == nil || err.Error() != want {
		t.Errorf("Cause = %v, want %q", err, want)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Cause = %v, want it to wrap context.DeadlineExceeded", err)
	}
}
