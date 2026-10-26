// Rexdiff shows the changes that a coding agent makes, beside the agent in
// Rex. You can comment on the changed lines, then paste the comments into the
// agent. "rexdiff install" adds a "Toggle rexdiff" action and a key to the Rex
// configuration, in two files beside Rex's init.lua. rexdiff.lua has the
// settings of rexdiff, and rexdiff_toggle.lua has the code of the action. The
// action gives the settings to rexdiff in the --config flag.
//
//	rexdiff [flags] [repo]
//	rexdiff install
//	rexdiff uninstall
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/rednafi/rexdiff/internal/proc"
	"github.com/rednafi/rexdiff/rex"
	"github.com/rednafi/rexdiff/tui"
	"github.com/rednafi/rexdiff/update"
	"github.com/rednafi/rexdiff/vcs"
)

func main() {
	// A closed pane sends SIGHUP. The stop of ctx lets rexdiff remove its
	// files before it exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	// rexdiff reads the environment once, here. The packages get it as a
	// value.
	err := run(ctx, os.Environ(), os.Args[1:], os.Stdout, os.Stderr)
	if err != nil && ctx.Err() != nil {
		// The error of a stopped program tells only "context canceled".
		// The cause names the signal.
		err = context.Cause(ctx)
	}
	stop()
	switch {
	case errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		os.Exit(2) // stderr already has the error
	case err != nil:
		fmt.Fprintln(os.Stderr, "rexdiff:", err)
		os.Exit(1)
	}
}

// errUsage is the error of a bad flag or argument. stderr already has the
// error and the usage.
var errUsage = errors.New("bad usage")

// usageError returns err of flag parsing for -h, and errUsage for the
// other errors.
func usageError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	return errUsage
}

// usage is the text above the flags in the help of the diff viewer.
const usage = `usage: rexdiff [flags] [repo]
       rexdiff install
       rexdiff uninstall

flags:
`

// run runs the subcommand in args, or else the diff viewer. env is the
// environment of rexdiff, as "key=value" pairs.
func run(ctx context.Context, env, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "install":
			bin, err := executable()
			if err != nil {
				return err
			}
			return install(ctx, env, bin, args[1:], stdout, stderr)
		case "uninstall":
			return uninstall(ctx, env, args[1:], stdout, stderr)
		}
	}
	f, err := parseFlags(args, env, stderr)
	if err != nil {
		return err
	}
	if f.version {
		fmt.Fprintln(stdout, cmp.Or(update.Version(version), "(devel)"))
		return nil
	}
	if f.check {
		return tui.CheckConfig(f.config)
	}
	if err := tui.CheckFlags(tui.Layout(f.layout), tui.Scope(f.scope)); err != nil {
		return err
	}

	// The programs that rexdiff starts, such as an editor, do not get the
	// variable of the resume file.
	resumePath, env := takeResume(env)
	dir := cmp.Or(f.repo, ".")
	repo, err := vcs.Open(ctx, env, dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = repo.Close() }()
	log, closeLog, err := openLog(f.logPath)
	if err != nil {
		return err
	}
	defer closeLog()

	// In Rex, upgrade writes rexdiff_toggle.lua if it differs from the code
	// of this rexdiff.
	client := rex.New(env)
	var notice string
	bin, binErr := executable()
	if binErr == nil && client != nil {
		notice = upgrade(ctx, env, bin)
	}
	resume, err := tui.Run(ctx, tui.Options{
		Repo:    repo,
		Rex:     client,
		Notice:  notice,
		Updater: update.Detect(env, bin, version),
		Resume:  resumePath,
		Env:     env,
		Config:  f.config,
		Layout:  tui.Layout(f.layout),
		Scope:   tui.Scope(f.scope),
		Agent:   f.agent,
		Base:    f.base,
		Wrap:    f.wrap,
		Log:     log,
	})
	if err != nil || resume == "" {
		return err
	}
	// After an update, the new rexdiff runs in this process.
	closeLog()
	_ = repo.Close() // the new process does not run the deferred calls
	return restart(bin, args, env, resume, stdout)
}

// restart runs the new rexdiff at bin in this process after an update,
// with the same args and env. resumeEnv gives it the saved state in the
// file resume. restart returns only if it fails.
func restart(bin string, args, env []string, resume string, stdout io.Writer) error {
	env = slices.Concat(env, []string{resumeEnv + "=" + resume})
	err := update.Restart(bin, args, env) // returns only if it fails
	// Run keeps the alternate screen for the restart. Leave that screen and
	// show the cursor.
	fmt.Fprint(stdout, "\x1b[?1049l\x1b[?25h")
	return fmt.Errorf("start the new rexdiff (your comments are saved in %s): %w", resume, err)
}

// resumeEnv is the variable that gives a restarted rexdiff the path of the
// file with the state from before the update. rexdiff uses a variable, not a
// flag, so that the arguments of the user do not change.
const resumeEnv = "REXDIFF_RESUME"

// takeResume returns the file in the variable resumeEnv of env, or "". It
// also returns a copy of env without the variable.
func takeResume(env []string) (path string, rest []string) {
	path = proc.Getenv(env, resumeEnv)
	isResume := func(kv string) bool { return strings.HasPrefix(kv, resumeEnv+"=") }
	return path, slices.DeleteFunc(slices.Clone(env), isResume)
}

// version is the version of rexdiff that GoReleaser sets, for example
// "0.2.0". It is "" in other builds.
var version string

// flags are the command-line arguments of the diff viewer.
type flags struct {
	layout  string
	scope   string
	wrap    bool
	base    string
	agent   string
	logPath string
	config  string // the settings as JSON, or ""
	check   bool   // check the settings and exit
	version bool   // print the version and exit
	repo    string // the repo argument, or ""
}

// parseFlags parses the arguments of the diff viewer. The defaults of
// -agent and -log come from env. It writes the usage and the errors of the
// flags to stderr. For -h, it returns [flag.ErrHelp], and for a bad flag
// or argument errUsage.
func parseFlags(args, env []string, stderr io.Writer) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("rexdiff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// tui.Run checks the layout and the scope, as in the settings.
	fs.StringVar(&f.layout, "layout", "", "diff layout: unified (top to bottom) or split (side by side)")
	fs.StringVar(&f.scope, "scope", "", "start scope: uncommitted, branch, or last-turn")
	// As in herdr-reviewr, -wrap takes on or off, so "-wrap off" works.
	wrap := fs.String("wrap", "on", "wrap of long lines: on or off (the key w toggles it)")
	fs.StringVar(&f.base, "base", "", "base for the branch scope (default: the picked base, else the repo's default branch)")
	fs.StringVar(&f.agent, "agent", proc.Getenv(env, "REXDIFF_AGENT"), "Rex block (ID or label) of the agent beside this pane")
	fs.StringVar(&f.logPath, "log", proc.Getenv(env, "REXDIFF_LOG"), "file for a debug log")
	fs.StringVar(&f.config, "config", "", "settings as JSON (the Rex action gives them from rexdiff.lua)")
	fs.BoolVar(&f.check, "check", false, "settings check only: check -config, then exit")
	fs.BoolVar(&f.version, "version", false, "version only: print it, then exit")
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flags{}, usageError(err)
	}
	if fs.NArg() > 1 {
		// Show the usage, as the flag package does for a bad flag.
		fmt.Fprintf(stderr, "unknown argument %q (flags go before the repo)\n", fs.Arg(1))
		fs.Usage()
		return flags{}, errUsage
	}
	if *wrap != "on" && *wrap != "off" {
		return flags{}, fmt.Errorf("invalid value for -wrap: expected one of on, off, not %q", *wrap)
	}
	f.wrap, f.repo = *wrap == "on", fs.Arg(0)
	return f, nil
}

// openLog returns a logger that appends to the file at path, and a function
// that closes the file. If path is empty, the logger discards everything.
// The log is only for debugging, so the close ignores errors.
func openLog(path string) (*slog.Logger, func(), error) {
	if path == "" {
		return slog.New(slog.DiscardHandler), func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), func() { _ = f.Close() }, nil
}
