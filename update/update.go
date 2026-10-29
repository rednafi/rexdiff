// Package update finds a newer release of rexdiff, installs it, and starts
// it in place of the running program.
//
// An [Installer] installs the latest [Release] the way rexdiff was
// installed: with Homebrew, go install, or a checked download from GitHub.
// [Restart] runs the new binary in the current process. The environment is
// passed as a value, but net/http still reads proxy variables from the
// process environment.
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
	"github.com/rednafi/rexdiff/internal/xctx"
)

// latestURL is the GitHub API URL of the latest release.
// REXDIFF_LATEST_URL overrides it; see [NewInstaller].
const latestURL = "https://api.github.com/repos/rednafi/rexdiff/releases/latest"

// modulePath is the import path of the main package, for go install.
const modulePath = "github.com/rednafi/rexdiff/cmd/rexdiff"

// Timeouts and size limits for downloads and commands. Sizes are in bytes.
const (
	fetchTimeout   = 5 * time.Second
	installTimeout = 5 * time.Minute
	maxArchive     = 64 << 20  // the largest archive that download accepts
	maxBinary      = 128 << 20 // the largest binary that an archive can contain
	maxMetadata    = 1 << 20   // the largest release JSON or checksum file
)

// method is how the user installed rexdiff, and so how an update installs
// it.
type method string

const (
	methodBrew     method = "brew"     // a Homebrew cask
	methodGo       method = "go"       // go install
	methodDownload method = "download" // a release archive from GitHub
)

// Release is a release of rexdiff on GitHub, as returned by
// [Installer.Latest]. A Release created any other way has no download
// files, so Install can install it only with Homebrew or go install.
type Release struct {
	Tag    string // for example "v0.2.0"
	assets []asset
}

// asset is a file attached to a GitHub release.
type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Installer installs releases of rexdiff. Use [NewInstaller] to create one.
type Installer struct {
	bin       string // the path that the user runs rexdiff from, possibly a symlink
	version   string // the version of the running rexdiff, for example "v0.1.0"
	method    method
	brew      string   // the brew that owns the cask of bin, for methodBrew
	env       []string // the environment for brew, go and rexdiff
	latestURL string
}

// Version returns the version of rexdiff, for example "v0.2.0". stamped is
// the version that a release build embeds, for example "0.2.0", or "". If
// stamped is "", Version returns the module version that go install
// records. For a development build, Version returns "".
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

// NewInstaller returns an Installer for the rexdiff binary at bin. If
// rexdiff cannot update itself, as in a development build, it returns nil
// and false. stamped is as for [Version]. The Installer runs brew, go and
// rexdiff with env, and fetches the latest release from
// REXDIFF_LATEST_URL if env sets it.
func NewInstaller(env []string, bin, stamped string) (*Installer, bool) {
	version := Version(stamped)
	if _, valid := numbers(version); bin == "" || !valid {
		return nil, false
	}
	u := &Installer{
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
		// The brew that owns the cask is in the bin directory next to the
		// Caskroom, for example /opt/homebrew/bin/brew.
		prefix, _, _ := strings.Cut(target, caskroom)
		u.brew = tool("brew", filepath.Join(prefix, "bin"))
	case stamped == "": // the version comes from go install
		u.method = methodGo
	}
	return u, true
}

// Latest returns the latest release and reports whether it is newer than
// the running rexdiff. Latest times out after 5 seconds.
func (in *Installer) Latest(ctx context.Context) (r Release, newer bool, err error) {
	ctx, cancel := xctx.WithTimeout(ctx, fetchTimeout, "release check")
	defer cancel()
	body, err := get(ctx, in.latestURL, maxMetadata)
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
	r = Release{Tag: wire.Tag, assets: wire.Assets}
	return r, isNewer(r.Tag, in.version), nil
}

// Install installs the release r and checks that the new binary reports
// the version of r. It does not stop the running rexdiff. Install ignores
// cancellation of ctx, because an install that stops halfway can leave a
// broken cask or binary. Install times out after 5 minutes.
func (in *Installer) Install(ctx context.Context, r Release) error {
	ctx, cancel := xctx.WithTimeout(context.WithoutCancel(ctx), installTimeout, "install")
	defer cancel()
	var err error
	switch in.method {
	case methodBrew:
		// Turn off brew's hints and prompts, because no one sees the output.
		env := []string{"HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1"}
		brew := cmp.Or(in.brew, "brew")
		// brew updates its taps at most once a day, so brew upgrade can
		// miss a new release. Update the taps first, then set
		// HOMEBREW_NO_AUTO_UPDATE so that upgrade does not update them again.
		if _, err = in.run(ctx, env, brew, "update", "--quiet"); err == nil {
			env = append(env, "HOMEBREW_NO_AUTO_UPDATE=1")
			_, err = in.run(ctx, env, brew, "upgrade", "--cask", "rexdiff")
		}
	case methodGo:
		// GOBIN makes go install write the new binary next to the old one.
		env := []string{"GOBIN=" + filepath.Dir(in.bin)}
		goTool := "go"
		// Rex starts panes with a PATH that does not include Go.
		if _, lerr := proc.LookPath(in.env, goTool); lerr != nil {
			goTool = tool("go", "/opt/homebrew/bin", "/usr/local/go/bin", "/usr/local/bin")
		}
		_, err = in.run(ctx, env, goTool, "install", modulePath+"@"+r.Tag)
	default:
		return in.download(ctx, r) // download checks the version before it replaces the binary
	}
	if err != nil {
		return err
	}
	return in.verifyVersion(ctx, in.bin, r.Tag)
}

// verifyVersion checks that the binary at bin reports the version tag.
func (in *Installer) verifyVersion(ctx context.Context, bin, tag string) error {
	out, err := in.run(ctx, nil, bin, "-version")
	if err != nil {
		// The error from run already names rexdiff.
		return fmt.Errorf("check the new version: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != tag {
		return fmt.Errorf("the update left rexdiff %s, not %s", cmp.Or(got, "of no version"), tag)
	}
	return nil
}

// run runs the program name with args, adding env to the Installer's
// environment, and returns its combined output. The error includes the
// last line of the output.
func (in *Installer) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := proc.Command(ctx, slices.Concat(in.env, env), name, args...)
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

// tool returns the path of the program name in one of dirs, or name itself
// if no dir has it. It exists because Rex starts panes with a PATH that
// does not include Homebrew or Go.
func tool(name string, dirs ...string) string {
	env := []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator))}
	if path, err := proc.LookPath(env, name); err == nil {
		return path
	}
	return name
}

// download downloads the archive of r for this operating system and
// architecture, verifies it against the checksums of r, and replaces the
// file that in.bin links to with the binary from the archive.
func (in *Installer) download(ctx context.Context, r Release) error {
	name := fmt.Sprintf("rexdiff-%s-%s-%s.tar.gz", r.Tag, runtime.GOOS, runtime.GOARCH)
	archiveURL, sumsURL := assetURL(r, name), assetURL(r, "sha256sums.txt")
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("release %s has no %s or sha256sums.txt", r.Tag, name)
	}
	sums, err := get(ctx, sumsURL, maxMetadata)
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
	target, err := filepath.EvalSymlinks(in.bin)
	if err != nil {
		return err
	}
	check := func(tmp string) error { return in.verifyVersion(ctx, tmp, r.Tag) }
	return replaceFile(target, bin, check)
}

// assetURL returns the download URL of the asset of r called name, or "".
func assetURL(r Release, name string) string {
	if i := slices.IndexFunc(r.assets, func(a asset) bool { return a.Name == name }); i >= 0 {
		return r.assets[i].URL
	}
	return ""
}

// checksum returns the hex SHA-256 sum that sums, the content of a
// sha256sums.txt file, lists for the file name.
func checksum(sums []byte, name string) (string, bool) {
	for line := range strings.SplitSeq(string(sums), "\n") {
		sum, file, ok := strings.Cut(strings.TrimSpace(line), "  ")
		if ok && file == name {
			return sum, true
		}
	}
	return "", false
}

// binaryOf returns the content of the regular file named rexdiff in a
// .tar.gz archive.
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

// replaceFile replaces the file at path with content using a single
// rename, so path is never half written. It first calls check on the new
// file. If check fails, path does not change.
func replaceFile(path string, content []byte, check func(tmp string) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rexdiff-update-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op after the rename
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

// get fetches url and returns the response body. It returns an error if
// the body is larger than limit bytes.
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

// Restart replaces the current process with the rexdiff at bin, run with
// env and args. The process keeps its ID and terminal. Restart returns
// only if it fails.
func Restart(env []string, bin string, args []string) error {
	return syscall.Exec(bin, slices.Concat([]string{bin}, args), env)
}

// isNewer reports whether the version tag is newer than the version have.
// Both have the form "v1.2.3". If either does not, isNewer reports false.
func isNewer(tag, have string) bool {
	t, ok1 := numbers(tag)
	h, ok2 := numbers(have)
	return ok1 && ok2 && slices.Compare(t[:], h[:]) > 0
}

// numbers returns the three numbers in a version such as "v1.2.3", and
// false if v does not have that form.
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
		// ParseUint, unlike Atoi, rejects a sign. The bit size keeps x
		// within the range of an int.
		x, err := strconv.ParseUint(p, 10, strconv.IntSize-1)
		if err != nil {
			return n, false
		}
		n[i] = int(x)
	}
	return n, true
}
