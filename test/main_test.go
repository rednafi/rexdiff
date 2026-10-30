package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
)

var (
	buildDir string
	homeDir  string // an empty directory that rexdiff uses as HOME
)

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
