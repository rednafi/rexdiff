package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
)

var (
	buildDir string // the directory of the binaries that the tests build
	homeDir  string // the HOME of rexdiff, an empty directory
)

// TestMain makes the directories of the binaries and of HOME. Then it
// runs the tests with testgit.
func TestMain(m *testing.M) {
	var err error
	if buildDir, err = os.MkdirTemp("", "rexdiff-test"); err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(buildDir) }()
	homeDir = filepath.Join(buildDir, "home")
	if err := os.Mkdir(homeDir, 0o755); err != nil {
		panic(err)
	}
	testgit.Main(m)
}
