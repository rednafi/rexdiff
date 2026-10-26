// Package update finds a newer release of rexdiff, installs it, and starts
// it in place of the running program.
//
// [Detect] finds how the user installed rexdiff. Its [Updater] finds the
// latest [Release] and installs it the same way. It uses Homebrew or go
// install. Else it downloads the archive of the release from GitHub and
// checks it with the checksums of the release. [Restart] runs the new binary
// in the same process, so the terminal and the process ID stay.
//
// The caller gives the environment as a value, as in [exec.Cmd.Env]. The
// package does not read the environment of the process.
package update

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rednafi/rexdiff/internal/proc"
)

// latestURL is the GitHub API address of the latest release of rexdiff.
// REXDIFF_LATEST_URL replaces it (see [Detect]).
const latestURL = "https://api.github.com/repos/rednafi/rexdiff/releases/latest"

// modulePath is the path of the main package, for go install.
const modulePath = "github.com/rednafi/rexdiff/cmd/rexdiff"

// Limits of the network and of the commands. The sizes are in bytes.
const (
	checkTimeout   = 5 * time.Second
	installTimeout = 5 * time.Minute
	maxArchive     = 64 << 20  // the largest archive that download accepts
	maxBinary      = 128 << 20 // the largest binary in an archive
)

// method is how the user installed rexdiff. An update installs it the same
// way.
type method string

const (
	methodBrew     method = "brew"     // a Homebrew cask
	methodGo       method = "go"       // go install
	methodDownload method = "download" // an archive of a release
)

// Release is a release of rexdiff on GitHub. Get one from [Updater.Latest].
// A Release that you make yourself has no files. Install can install such a
// Release with Homebrew or go install. Install cannot download it.
type Release struct {
	Tag    string // for example "v0.2.0"
	assets []asset
}

// asset is a file that is attached to a GitHub release.
type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Updater installs releases of rexdiff. Use [Detect] to make one.
type Updater struct {
	bin       string // the path of rexdiff as the user runs it, maybe a symlink
	version   string // the version of the running rexdiff, for example "v0.1.0"
	method    method
	brew      string   // the brew of the Caskroom of bin, for methodBrew
	env       []string // the environment of brew, go, and rexdiff
	latestURL string
}

// Version returns the version of rexdiff, for example "v0.2.0". stamped is
// the version that the release build sets, for example "0.2.0", or "". If
// stamped is "", Version returns the module version that go install
// records. For a build for development, Version returns "".
func Version(stamped string) string {
	if stamped != "" {
		return "v" + strings.TrimPrefix(stamped, "v")
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if _, valid := numbers(info.Main.Version); valid {
			return info.Main.Version
		}
	}
	return ""
}

// Detect returns the Updater of the rexdiff at bin. stamped is as for
// [Version]. Detect returns nil if rexdiff cannot update itself, for
// example a build for development.
//
// The Updater installs a release the same way as the user installed
// rexdiff. If bin links into a Caskroom, it uses Homebrew. If stamped is ""
// and the version comes from go install, it uses go install. Else it
// downloads the release. The Updater runs brew, go, and rexdiff with the
// environment env, and finds them in the PATH of env. If env sets
// REXDIFF_LATEST_URL, the Updater gets the latest release from that
// address. Set it to test a release before it is public.
func Detect(bin, stamped string, env []string) *Updater {
	return detect(bin, Version(stamped), stamped == "", env)
}

// detect is [Detect] with the version already found. goInstall is true if
// the version comes from go install.
func detect(bin, version string, goInstall bool, env []string) *Updater {
	if _, valid := numbers(version); bin == "" || !valid {
		return nil
	}
	u := &Updater{
		bin:       bin,
		version:   version,
		method:    methodDownload,
		env:       env,
		latestURL: cmp.Or(proc.Getenv(env, "REXDIFF_LATEST_URL"), latestURL),
	}
	target, err := filepath.EvalSymlinks(bin)
	caskroom := string(filepath.Separator) + "Caskroom" + string(filepath.Separator)
	switch {
	case err == nil && strings.Contains(target, caskroom):
		u.method = methodBrew
		// The brew that owns the cask is in the bin directory beside the
		// Caskroom, for example /opt/homebrew/bin/brew.
		prefix, _, _ := strings.Cut(target, caskroom)
		u.brew = tool("brew", filepath.Join(prefix, "bin"))
	case goInstall:
		u.method = methodGo
	}
	return u
}

// Latest returns the latest release, and whether it is newer than the
// running rexdiff. Latest stops and returns an error after five seconds.
func (u *Updater) Latest(ctx context.Context) (Release, bool, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, checkTimeout,
		fmt.Errorf("release check: %v timeout: %w", checkTimeout, context.DeadlineExceeded))
	defer cancelTimeout()
	defer cancel(fmt.Errorf("release check: done: %w", context.Canceled))
	body, err := get(ctx, u.latestURL, 1<<20)
	if err != nil {
		return Release{}, false, err
	}
	var wire struct {
		Tag    string  `json:"tag_name"`
		Assets []asset `json:"assets"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return Release{}, false, fmt.Errorf("read the latest release: %w", err)
	}
	r := Release{Tag: wire.Tag, assets: wire.Assets}
	return r, newer(r.Tag, u.version), nil
}

// Install installs the release r. Then it checks that the binary of rexdiff
// has the version of r. Install does not stop the running rexdiff. Install
// uses the values of ctx, but ignores its cancel. An install that stops
// early can leave a broken cask or binary. Install stops after five
// minutes.
func (u *Updater) Install(ctx context.Context, r Release) error {
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, installTimeout,
		fmt.Errorf("install: %v timeout: %w", installTimeout, context.DeadlineExceeded))
	defer cancelTimeout()
	defer cancel(fmt.Errorf("install: done: %w", context.Canceled))
	var err error
	switch u.method {
	case methodBrew:
		// Turn off the hints and the prompts of brew, because no user sees
		// the output.
		env := []string{"HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1"}
		brew := cmp.Or(u.brew, "brew")
		// brew updates its taps at most once a day. Thus brew upgrade can
		// miss a new release. Update the taps first. Then
		// HOMEBREW_NO_AUTO_UPDATE stops a second update.
		if _, err = u.runProgram(ctx, env, brew, "update", "--quiet"); err == nil {
			env = append(env, "HOMEBREW_NO_AUTO_UPDATE=1")
			_, err = u.runProgram(ctx, env, brew, "upgrade", "--cask", "rexdiff")
		}
	case methodGo:
		// GOBIN puts the new binary where the old one is.
		env := []string{"GOBIN=" + filepath.Dir(u.bin)}
		goTool := "go"
		// Rex starts panes with a PATH without Go.
		if _, lerr := proc.LookPath(u.env, goTool); lerr != nil {
			goTool = tool("go", "/opt/homebrew/bin", "/usr/local/go/bin", "/usr/local/bin")
		}
		_, err = u.runProgram(ctx, env, goTool, "install", modulePath+"@"+r.Tag)
	default:
		return u.download(ctx, r) // it checks the version before the replace
	}
	if err != nil {
		return err
	}
	return u.checkVersion(ctx, u.bin, r.Tag)
}

// checkVersion checks that the binary at bin is the version tag, so that a
// restart does not run a broken or an old binary.
func (u *Updater) checkVersion(ctx context.Context, bin, tag string) error {
	out, err := u.runProgram(ctx, nil, bin, "-version")
	if err != nil {
		return fmt.Errorf("check the new rexdiff: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != tag {
		return fmt.Errorf("the update left rexdiff %s, not %s", cmp.Or(got, "of no version"), tag)
	}
	return nil
}

// runProgram runs name with args, with env added to the environment of u.
// Its error has the end of the output, which tells what went wrong.
func (u *Updater) runProgram(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	// A copy keeps u.env as it is.
	cmd := proc.Command(ctx, slices.Concat(u.env, env), name, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return out, proc.NotInstalled(name)
	}
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s: %w", filepath.Base(name), proc.Error(ctx, name, err, nil))
	}
	if last := proc.LastLine(string(out)); last != "" {
		return out, fmt.Errorf("%s: %s: %w", filepath.Base(name), last, err)
	}
	return out, fmt.Errorf("%s: %w", filepath.Base(name), err)
}

// tool returns the path of the program name in dirs. If dirs do not have
// the program, tool returns name, and the Updater looks for name in the
// PATH of its environment. tool looks in dirs first, because Rex starts
// panes with a PATH without Homebrew or Go.
func tool(name string, dirs ...string) string {
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return path
		}
	}
	return name
}

// download downloads the archive of r for this system and checks it with
// the checksums of r. Then it puts its binary in place of the file that
// u.bin links to.
func (u *Updater) download(ctx context.Context, r Release) error {
	name := fmt.Sprintf("rexdiff-%s-%s-%s.tar.gz", r.Tag, runtime.GOOS, runtime.GOARCH)
	archiveURL, sumsURL := assetURL(r, name), assetURL(r, "sha256sums.txt")
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("release %s has no %s or sha256sums.txt", r.Tag, name)
	}
	sums, err := get(ctx, sumsURL, 1<<20)
	if err != nil {
		return err
	}
	want, ok := checksum(sums, name)
	if !ok {
		return fmt.Errorf("sha256sums.txt of %s has no %s", r.Tag, name)
	}
	archive, err := get(ctx, archiveURL, maxArchive)
	if err != nil {
		return err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s does not match its checksum", name)
	}
	bin, err := binaryOf(archive)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	target, err := filepath.EvalSymlinks(u.bin)
	if err != nil {
		return err
	}
	check := func(tmp string) error { return u.checkVersion(ctx, tmp, r.Tag) }
	return replaceFile(target, bin, check)
}

// assetURL returns the address of the asset of r with the name, or "".
func assetURL(r Release, name string) string {
	if i := slices.IndexFunc(r.assets, func(a asset) bool { return a.Name == name }); i >= 0 {
		return r.assets[i].URL
	}
	return ""
}

// checksum returns the hex SHA-256 of the file name in a sha256sums.txt.
func checksum(sums []byte, name string) (string, bool) {
	for line := range strings.SplitSeq(string(sums), "\n") {
		sum, file, ok := strings.Cut(strings.TrimSpace(line), "  ")
		if ok && file == name {
			return sum, true
		}
	}
	return "", false
}

// binaryOf returns the content of the file rexdiff in a .tar.gz archive.
func binaryOf(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the archive has no rexdiff")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "rexdiff" {
			b, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
			if err != nil {
				return nil, err
			}
			if len(b) > maxBinary {
				return nil, fmt.Errorf("rexdiff is larger than %d bytes", maxBinary)
			}
			return b, nil
		}
	}
}

// replaceFile puts content in place of the file at path, in one rename, so
// that path is never a half-written file. check runs on the new file first.
// If check fails, path does not change.
func replaceFile(path string, content []byte, check func(tmp string) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rexdiff-update-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // fails after the rename
	_, err = tmp.Write(content)
	if err == nil {
		err = tmp.Chmod(0o755)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := check(tmp.Name()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// get returns the body at url, of at most limit bytes.
func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json, */*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s: more than %d bytes", url, limit)
	}
	return body, nil
}

// Restart runs the rexdiff at bin with args and the environment env in
// place of this process. The process ID and the terminal stay, so Rex sees
// the same program. Restart returns only if it fails.
func Restart(bin string, args, env []string) error {
	return syscall.Exec(bin, append([]string{bin}, args...), env)
}

// newer reports whether the version tag is newer than the version have.
// Both are like "v1.2.3". A version that is not "v" and three numbers is
// never newer. Thus a build for development does not ask to update. Also,
// rexdiff does not offer a tag that names no archive of GoReleaser.
func newer(tag, have string) bool {
	t, ok1 := numbers(tag)
	h, ok2 := numbers(have)
	return ok1 && ok2 && slices.Compare(t[:], h[:]) > 0
}

// numbers returns the three numbers of a version like "v1.2.3".
func numbers(v string) ([3]int, bool) {
	var n [3]int
	v, ok := strings.CutPrefix(v, "v")
	if !ok {
		return n, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != len(n) {
		return n, false
	}
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return n, false // Atoi accepts a sign, but a version has none
		}
		x, err := strconv.Atoi(p)
		if err != nil {
			return n, false
		}
		n[i] = x
	}
	return n, true
}
