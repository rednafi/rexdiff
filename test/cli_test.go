package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// fakeRexCLI is the rex of the scripts.
const fakeRexCLI = `#!/bin/sh
printf '%s\n' "$*" >> "$WORK/rex.log"
if [ -n "$REX_FAIL" ]; then echo 'no Rex server' >&2; exit 1; fi
echo 'config reloaded'
`

// TestScripts runs the scripts in testdata/script. A script runs the
// rexdiff binary and a fake rex. The fake writes its arguments to
// $WORK/rex.log, and fails if $REX_FAIL is set. If REXDIFF_REX is the
// path of a real rex, $REALREX is that rex, and the condition [realrex] is
// true.
func TestScripts(t *testing.T) {
	bin := filepath.Dir(binary(t))
	realRex := os.Getenv("REXDIFF_REX")
	hasRex := realRex != ""
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		Setup: func(env *testscript.Env) error {
			fake := filepath.Join(env.WorkDir, ".fake")
			if err := os.Mkdir(fake, 0o755); err != nil {
				return err
			}
			err := os.WriteFile(filepath.Join(fake, "rex"), []byte(fakeRexCLI), 0o755)
			if err != nil {
				return err
			}
			// $WORK is also $HOME, so rexdiff and git see no file of the
			// user.
			env.Setenv("HOME", env.WorkDir)
			env.Setenv("XDG_CONFIG_HOME", "")
			env.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			for _, k := range userEnv {
				env.Setenv(k, "")
			}
			env.Setenv("REX_BIN_DIR", fake)
			env.Setenv("REALREX", realRex)
			for _, kv := range testgit.Env() {
				k, v, _ := strings.Cut(kv, "=")
				env.Setenv(k, v)
			}
			return nil
		},
		Condition: func(cond string) (bool, error) {
			if cond != "realrex" {
				return false, fmt.Errorf("unknown condition %q", cond)
			}
			return hasRex, nil
		},
	})
}
