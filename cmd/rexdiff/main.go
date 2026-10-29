// Command rexdiff shows the changes that a coding agent makes, in a Rex
// pane next to the agent. You can comment on changed lines and paste the
// comments into the agent. "rexdiff install" adds a "Toggle rexdiff" action and a
// key binding to the Rex configuration. The action passes the settings
// from rexdiff.lua to rexdiff with the --config flag.
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
	// Closing the pane sends SIGHUP. Cancel ctx on these signals so rexdiff
	// can remove its temporary files before it exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	// Read the environment only here, and pass it to the packages as a
	// value.
	err := run(ctx, os.Environ(), os.Args[1:], os.Stdout, os.Stderr)
	if err != nil && ctx.Err() != nil {
		// The error says only "context canceled", but the cause names the
		// signal.
		err = context.Cause(ctx)
	}
	stop()
	switch {
	case errors.Is(err, flag.ErrHelp):
		// -h printed the usage, which is a success.
	case errors.Is(err, errUsage):
		os.Exit(exitUsage) // the error is already printed on stderr
	case err != nil:
		fmt.Fprintln(os.Stderr, "rexdiff:", err)
		os.Exit(1)
	}
}

// exitUsage is the exit status for an invalid flag or argument, as with
// the flag package.
const exitUsage = 2

// errUsage is the error for an invalid flag or argument. By the time it is
// returned, the error message and the usage are already printed on
// stderr.
var errUsage = errors.New("bad usage")

// usageError returns err for -h, and errUsage for other flag errors.
func usageError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	return errUsage
}

// usage is the help text printed before the list of the diff viewer's
// flags.
const usage = `usage: rexdiff [flags] [repo]
       rexdiff install
       rexdiff uninstall

flags:
`

// run runs the subcommand named in args, or the diff viewer if there is
// none. env is the environment of rexdiff, as "key=value" pairs.
func run(ctx context.Context, env, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "install":
			bin, err := executablePath()
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
	if err := tui.ValidateFlags(tui.Layout(f.layout), tui.Scope(f.scope)); err != nil {
		return err
	}
	if f.check {
		return tui.ValidateConfig(f.config)
	}

	// Remove the resume variable, so programs that rexdiff starts, such as
	// an editor, do not inherit it.
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

	// When running in Rex, update rexdiff_toggle.lua if it differs from
	// the code that this rexdiff would write.
	client, inRex := rex.New(env)
	var notice string
	bin, binErr := executablePath()
	if binErr == nil && inRex {
		notice = migrateLua(ctx, env, bin)
	}
	installer, _ := update.NewInstaller(env, bin, version) // nil if rexdiff cannot update itself
	resume, err := tui.Run(ctx, tui.Options{
		Repo:      repo,
		Rex:       client,
		Notice:    notice,
		Installer: installer,
		Resume:    resumePath,
		Env:       env,
		Config:    f.config,
		Layout:    tui.Layout(f.layout),
		Scope:     tui.Scope(f.scope),
		Agent:     f.agent,
		Base:      f.base,
		Wrap:      f.wrap,
		Log:       log,
	})
	if err != nil || resume == "" {
		return err
	}
	// After an update, the new rexdiff replaces this process.
	closeLog()
	_ = repo.Close() // the deferred calls never run once the process is replaced
	return restart(env, bin, args, resume, stdout)
}

// restart replaces this process with the new rexdiff at bin after an
// update. The new rexdiff gets the same env and args, plus the path of the
// file resume, which holds the saved state. restart returns only if it
// fails.
func restart(env []string, bin string, args []string, resume string, stdout io.Writer) error {
	env = slices.Concat(env, []string{resumeEnv + "=" + resume})
	err := update.Restart(env, bin, args) // returns only if it fails
	// tui.Run leaves the terminal on the alternate screen for the new
	// process. Since the restart failed, switch back and show the cursor.
	fmt.Fprint(stdout, "\x1b[?1049l\x1b[?25h")
	return fmt.Errorf("start the new rexdiff (your comments are saved in %s): %w", resume, err)
}

// resumeEnv is the environment variable that tells a restarted rexdiff
// where to find the state saved before the update. It is an environment
// variable, not a flag, so the user's arguments stay the same.
const resumeEnv = "REXDIFF_RESUME"

// takeResume returns the value of resumeEnv in env, or "" if it is not
// set, and a copy of env without that variable.
func takeResume(env []string) (path string, rest []string) {
	path = proc.Getenv(env, resumeEnv)
	isResume := func(kv string) bool { return strings.HasPrefix(kv, resumeEnv+"=") }
	return path, slices.DeleteFunc(slices.Clone(env), isResume)
}

// version is the rexdiff version that GoReleaser sets at build time, for
// example "0.2.0". It is "" in other builds.
var version string

// flags holds the command-line arguments of the diff viewer.
type flags struct {
	layout  string
	scope   string
	wrap    bool
	base    string
	agent   string
	logPath string
	config  string // the settings as JSON, or ""
	check   bool   // validate the settings and exit
	version bool   // print the version and exit
	repo    string // the repository argument, or ""
}

// parseFlags parses the arguments of the diff viewer. The defaults for
// -agent and -log come from env. It prints the usage and flag errors on
// stderr. For -h it returns [flag.ErrHelp]. For an invalid flag or an
// extra argument it returns errUsage.
func parseFlags(args, env []string, stderr io.Writer) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("rexdiff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// The caller validates the layout and scope values.
	fs.StringVar(&f.layout, "layout", "", "diff layout: unified (top to bottom) or split (side by side)")
	fs.StringVar(&f.scope, "scope", "", "start scope: uncommitted, branch, or last-turn")
	// -wrap is a string flag that takes on or off, so that "-wrap off"
	// works. A boolean flag would need "-wrap=false".
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
		// Print the usage, as the flag package does for an unknown flag.
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
// that closes the file, ignoring any error. If path is empty, the logger
// discards all output. A new file is readable only by the user, because
// the log names files and agents. The path may also be a terminal or a
// FIFO, but opening a FIFO with no reader fails at once instead of
// waiting for one.
func openLog(path string) (*slog.Logger, func(), error) {
	if path == "" {
		return slog.New(slog.DiscardHandler), func() {}, nil
	}
	flags := os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NONBLOCK
	f, err := os.OpenFile(path, flags, 0o600)
	if pe, ok := errors.AsType[*os.PathError](err); ok {
		err = pe.Err // the path is in the message already
	}
	if errors.Is(err, syscall.ENXIO) {
		err = errors.New("the FIFO has no reader, start one first")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open log %s: %w", path, err)
	}
	h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), func() { _ = f.Close() }, nil
}
