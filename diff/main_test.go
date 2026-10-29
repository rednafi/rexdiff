package diff_test

import (
	"testing"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/testgit"
)

func TestMain(m *testing.M) { testgit.Main(m) }

// fileOptions returns the fields of o that [diff.RenderFile] uses.
func fileOptions(o diff.Options) diff.FileOptions {
	return diff.FileOptions{Width: o.Width, ScrollX: o.ScrollX, Wrap: o.Wrap, Cache: o.Cache}
}
