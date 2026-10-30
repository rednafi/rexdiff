// Package update finds, installs and restarts a newer release of rexdiff.
// net/http reads proxy variables from the process environment.
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

// latestURL can be overridden with REXDIFF_LATEST_URL.
const latestURL = "https://api.github.com/repos/rednafi/rexdiff/releases/latest"

const modulePath = "github.com/rednafi/rexdiff/cmd/rexdiff"

// Sizes are in bytes.
const (
	fetchTimeout   = 5 * time.Second
	installTimeout = 5 * time.Minute
	maxArchive     = 64 << 20
	maxBinary      = 128 << 20
	maxMetadata    = 1 << 20
)

type method string

const (
	methodBrew     method = "brew"
	methodGo       method = "go"
	methodDownload method = "download"
)

// Release is a rexdiff release on GitHub. Only a Release from
// [Installer.Latest] can install by download.
type Release struct {
	Tag    string
	assets []asset
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Installer installs releases of rexdiff. Use [NewInstaller] to create one.
type Installer struct {
	bin       string // the path that the user runs, possibly a symlink
	version   string
	method    method
	brew      string // the brew that owns the cask of bin, for methodBrew
	env       []string
	latestURL string
}

// Version returns a version such as "v0.2.0", or "" for a development
// build. stamped is the version that a release build embeds, or "".
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

// NewInstaller returns false if rexdiff cannot update itself. stamped is as
// for [Version].
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
		// The brew that owns the cask is in bin next to the Caskroom.
		prefix, _, _ := strings.Cut(target, caskroom)
		u.brew = tool("brew", filepath.Join(prefix, "bin"))
	case stamped == "": // the version comes from go install
		u.method = methodGo
	}
	return u, true
}

// Latest reports whether the latest release is newer than the running one.
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

// Install installs r and checks the new version. It ignores cancellation
// of ctx, because a half install can break the cask or the binary.
func (in *Installer) Install(ctx context.Context, r Release) error {
	ctx, cancel := xctx.WithTimeout(context.WithoutCancel(ctx), installTimeout, "install")
	defer cancel()
	var err error
	switch in.method {
	case methodBrew:
		// Turn off brew's hints and prompts, because no one sees the output.
		env := []string{"HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1"}
		brew := cmp.Or(in.brew, "brew")
		// brew updates its taps at most once a day, so upgrade can miss a
		// new release. Update first, then stop upgrade from updating again.
		if _, err = in.run(ctx, env, brew, "update", "--quiet"); err == nil {
			env = append(env, "HOMEBREW_NO_AUTO_UPDATE=1")
			_, err = in.run(ctx, env, brew, "upgrade", "--cask", "rexdiff")
		}
	case methodGo:
		env := []string{"GOBIN=" + filepath.Dir(in.bin)}
		goTool := "go"
		// Rex starts panes with a PATH that does not include Go.
		if _, lerr := proc.LookPath(in.env, goTool); lerr != nil {
			goTool = tool("go", "/opt/homebrew/bin", "/usr/local/go/bin", "/usr/local/bin")
		}
		_, err = in.run(ctx, env, goTool, "install", modulePath+"@"+r.Tag)
	default:
		return in.download(ctx, r) // checks the version before it replaces the binary
	}
	if err != nil {
		return err
	}
	return in.verifyVersion(ctx, in.bin, r.Tag)
}

func (in *Installer) verifyVersion(ctx context.Context, bin, tag string) error {
	out, err := in.run(ctx, nil, bin, "-version")
	if err != nil {
		return fmt.Errorf("check the new version: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != tag {
		return fmt.Errorf("the update left rexdiff %s, not %s", cmp.Or(got, "of no version"), tag)
	}
	return nil
}

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

// tool searches dirs because Rex starts panes with a PATH that does not
// include Homebrew or Go.
func tool(name string, dirs ...string) string {
	env := []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator))}
	if path, err := proc.LookPath(env, name); err == nil {
		return path
	}
	return name
}

// download replaces the file that in.bin links to, not the link.
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

func assetURL(r Release, name string) string {
	if i := slices.IndexFunc(r.assets, func(a asset) bool { return a.Name == name }); i >= 0 {
		return r.assets[i].URL
	}
	return ""
}

func checksum(sums []byte, name string) (string, bool) {
	for line := range strings.SplitSeq(string(sums), "\n") {
		sum, file, ok := strings.Cut(strings.TrimSpace(line), "  ")
		if ok && file == name {
			return sum, true
		}
	}
	return "", false
}

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

// replaceFile uses one rename, so path is never half written. If check
// fails on the new file, path does not change.
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

// Restart replaces the current process with bin. It returns only if it
// fails.
func Restart(env []string, bin string, args []string) error {
	return syscall.Exec(bin, slices.Concat([]string{bin}, args), env)
}

// isNewer reports false if either version is not like "v1.2.3".
func isNewer(tag, have string) bool {
	t, ok1 := numbers(tag)
	h, ok2 := numbers(have)
	return ok1 && ok2 && slices.Compare(t[:], h[:]) > 0
}

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
