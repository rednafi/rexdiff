package update_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/update"
)

// mkdir makes the directory dir and its parents, and returns dir.
func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// detect returns the Updater of the rexdiff at bin, with the latest
// release at latest. Its environment has REXDIFF_LATEST_URL and the PATH
// /usr/bin:/bin, then the variables of env. A later variable wins.
func detect(t *testing.T, latest, bin, stamped string, env ...string) *update.Updater {
	t.Helper()
	env = append([]string{"REXDIFF_LATEST_URL=" + latest, "PATH=/usr/bin:/bin"}, env...)
	u := update.Detect(env, bin, stamped)
	if u == nil {
		t.Fatalf("Detect(%q, %s, %s) = nil, want an Updater", env, bin, stamped)
	}
	return u
}

// serve starts a server with the handler h, and returns its address. The
// test stops the server at the end.
func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestVersion checks the version from the release build, and the builds
// that cannot update. A test binary has no version, like a build for
// development.
func TestVersion(t *testing.T) {
	t.Parallel()
	for stamped, want := range map[string]string{"0.2.0": "v0.2.0", "v0.2.0": "v0.2.0", "": ""} {
		if got := update.Version(stamped); got != want {
			t.Errorf("Version(%q) = %q, want %q", stamped, got, want)
		}
	}
	tests := []struct{ bin, stamped string }{
		{"/bin/rexdiff", ""},
		{"/bin/rexdiff", "(devel)"},
		{"/bin/rexdiff", "0.0.0-20261024-abcdef"},
		{"", "0.1.0"},
	}
	for _, tt := range tests {
		if u := update.Detect(nil, tt.bin, tt.stamped); u != nil {
			t.Errorf("Detect(nil, %q, %q) = %+v, want nil", tt.bin, tt.stamped, u)
		}
	}
}

// TestLatest checks Latest against a fake GitHub API, and which tags are
// newer than the running version.
func TestLatest(t *testing.T) {
	t.Parallel()
	release := func(tag string) string {
		return `{"tag_name": "` + tag + `", "assets": [], "other": 1}`
	}
	tests := []struct {
		name, have, body string // a body of "" is a 404
		tag              string // the tag of the release, or ""
		newer            bool
		errText          string // a part of the error, or ""
	}{
		{"minor", "0.1.0", release("v0.2.0"), "v0.2.0", true, ""},
		{"patch", "v0.1.0", release("v0.1.1"), "v0.1.1", true, ""},
		{"major", "0.9.9", release("v1.0.0"), "v1.0.0", true, ""},
		{"two digits", "0.9.0", release("v0.10.0"), "v0.10.0", true, ""},
		// GoReleaser names archives with the v.
		{"no v", "0.1.0", release("0.2.0"), "0.2.0", false, ""},
		{"same", "0.1.0", release("v0.1.0"), "v0.1.0", false, ""},
		{"older", "0.2.0", release("v0.1.0"), "v0.1.0", false, ""},
		{"release candidate", "0.1.0", release("v0.2.0-rc1"), "v0.2.0-rc1", false, ""},
		{"no tag", "0.1.0", release(""), "", false, ""},
		// Atoi accepts a sign, but a version has none.
		{"sign", "0.9.0", release("v+1.0.0"), "v+1.0.0", false, ""},
		{"minus", "0.9.0", release("v1.-0.0"), "v1.-0.0", false, ""},
		{"status", "0.1.0", "", "", false, "404"},
		{"bad JSON", "0.1.0", "not json", "", false, "read the latest release"},
	}
	files := fstest.MapFS{}
	for i, tt := range tests {
		if tt.body != "" {
			files[strconv.Itoa(i)] = &fstest.MapFile{Data: []byte(tt.body)}
		}
	}
	base := serve(t, http.FileServerFS(files))
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := detect(t, fmt.Sprint(base, "/", i), "/bin/rexdiff", tt.have)
			r, newer, err := u.Latest(t.Context())
			if tt.errText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errText) {
					t.Errorf("Latest: %v, want an error with %q", err, tt.errText)
				}
				return
			}
			if err != nil || r.Tag != tt.tag || newer != tt.newer {
				t.Errorf("Latest = %q, %v, %v, want %q, %v, nil",
					r.Tag, newer, err, tt.tag, tt.newer)
			}
		})
	}
}

// TestInstallBrew checks the commands of a Homebrew install, and the check
// of the new version.
func TestInstallBrew(t *testing.T) {
	t.Parallel()
	// The fake brew logs the flags of brew and its arguments. Rex starts
	// rexdiff with a PATH without Homebrew, so the brew beside the
	// Caskroom comes first. The environment of the Updater has none of the
	// flags of brew.
	const logBrew = `echo "hints=$HOMEBREW_NO_ENV_HINTS ` +
		`ni=$NONINTERACTIVE auto=$HOMEBREW_NO_AUTO_UPDATE ${0##*/} $*" ` +
		`>> "${0%/*}/../calls"` + "\n"
	const busy = "echo Updating Homebrew...\necho 'Error: another brew is running'\nexit 1\n"
	const calls = "hints=1 ni=1 auto= brew update --quiet\n" +
		"hints=1 ni=1 auto=1 brew upgrade --cask rexdiff\n"
	tests := []struct {
		name    string
		beside  bool   // the brew is beside the Caskroom, else in PATH
		brew    string // the script of the fake brew
		version string // the version of the new rexdiff
		cancel  bool   // cancel the context before Install
		calls   string // the log of brew, or "" for no check
		errText string // a part of the error, or "" for none
	}{
		{"beside the Caskroom", true, logBrew, "v0.2.0", false, calls, ""},
		{"in PATH", false, logBrew, "v0.2.0", false, calls, ""},
		// The error has the last line of the output.
		{
			"brew fails", true, busy, "v0.2.0", false, "",
			"brew: Error: another brew is running: exit status 1",
		},
		// The error has no empty part at its end.
		{
			"brew fails with no output", true, "exit 1\n", "v0.2.0", false, "",
			"brew: exit status 1",
		},
		// A new binary of another version is an error, so rexdiff does not
		// restart into it.
		{
			"old version", true, logBrew, "v0.1.0", false, "",
			"the update left rexdiff v0.1.0, not v0.2.0",
		},
		// A stopped brew can leave a broken cask. The user can quit
		// rexdiff while brew runs.
		{"outlives a cancel", true, logBrew, "v0.2.0", true, calls, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			prefix := t.TempDir()
			caskDir := mkdir(t, filepath.Join(prefix, "Caskroom", "rexdiff", "0.2.0"))
			cask := testgit.Script(t, caskDir, "rexdiff", "echo "+tt.version+"\n")
			path := filepath.Join(prefix, "path")
			if tt.beside {
				path = filepath.Join(prefix, "bin")
			}
			testgit.Script(t, mkdir(t, path), "brew", tt.brew)
			link := filepath.Join(mkdir(t, filepath.Join(prefix, "bin")), "rexdiff")
			if err := os.Symlink(cask, link); err != nil {
				t.Fatal(err)
			}
			pathEnv := "PATH=" + filepath.Join(prefix, "path") + ":/usr/bin:/bin"

			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(fmt.Errorf("test: done: %w", context.Canceled))
			if tt.cancel {
				cancel(fmt.Errorf("test stops the install: %w", context.Canceled))
			}
			u := detect(t, "http://127.0.0.1:1/latest", link, "0.1.0", pathEnv)
			err := u.Install(ctx, update.Release{Tag: "v0.2.0"})
			failed := err != nil
			if tt.errText != "" {
				failed = err == nil || !strings.Contains(err.Error(), tt.errText)
			}
			if failed {
				t.Errorf("Install = %v, want an error with %q", err, tt.errText)
			}
			if tt.calls == "" {
				return
			}
			got, err := os.ReadFile(filepath.Join(prefix, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.calls {
				t.Errorf("brew calls = \n%s\nwant:\n%s", got, tt.calls)
			}
		})
	}
}

// archive returns a .tar.gz with a README.md and a file rexdiff that has
// content.
func archive(t *testing.T, content string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	add := func(name, content string, mode int64) {
		t.Helper()
		hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	add("README.md", "hi", 0o644)
	add("rexdiff", content, 0o755)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestDownload checks the download of a release. It checks the checksum,
// the binary in the archive, and the check of its version. It also checks
// the replacement of the target file of a link.
func TestDownload(t *testing.T) {
	t.Parallel()
	name := fmt.Sprintf("rexdiff-v0.2.0-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	newBin := "#!/bin/sh\necho v0.2.0\n"
	good := archive(t, newBin)
	sums := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:]) + "  " + name + "\nabc  other.tar.gz\n"
	}
	stale := archive(t, "#!/bin/sh\necho v0.1.9\n")
	// The limit of a binary is 128 MiB.
	big := archive(t, strings.Repeat("x", 128<<20+1))
	files := fstest.MapFS{}
	for dir, a := range map[string][]byte{"good": good, "stale": stale, "big": big, "bad": good} {
		files[dir+"/"+name] = &fstest.MapFile{Data: a}
		files[dir+"/sha256sums.txt"] = &fstest.MapFile{Data: []byte(sums(a))}
	}
	files["bad/"+name] = &fstest.MapFile{Data: []byte("tampered")}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(files))
	// The release of a directory has its files, at the URLs of the server.
	// The release of "none" has no files.
	mux.HandleFunc("GET /{dir}/latest", func(w http.ResponseWriter, req *http.Request) {
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		dir := req.PathValue("dir")
		assets := []asset{}
		for _, f := range []string{name, "sha256sums.txt"} {
			if _, ok := files[dir+"/"+f]; ok {
				assets = append(assets, asset{f, "http://" + req.Host + "/" + dir + "/" + f})
			}
		}
		release := map[string]any{"tag_name": "v0.2.0", "assets": assets}
		if err := json.MarshalWrite(w, release); err != nil {
			t.Error(err)
		}
	})
	base := serve(t, mux)

	tests := []struct {
		name, dir string
		errText   string // a part of the error, or "" for none
	}{
		{"good", "good", ""},
		{"tampered", "bad", "does not match its checksum"},
		// The old binary stays, so the next start does not run a binary of
		// another version.
		{"other version", "stale", "the update left rexdiff v0.1.9, not v0.2.0"},
		// A binary over the limit is an error, not a binary that is cut.
		{"too large", "big", "larger than"},
		{"no archive", "none", "has no " + name},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			const oldBin = "echo v0.1.0\n"
			realDir := mkdir(t, filepath.Join(dir, "real"))
			target := testgit.Script(t, realDir, "rexdiff", oldBin)
			link := filepath.Join(dir, "rexdiff")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			u := detect(t, base+"/"+tt.dir+"/latest", link, "0.1.0")
			r, newer, err := u.Latest(t.Context())
			if err != nil || !newer {
				t.Fatalf("Latest = %+v, %v, %v, want a newer release", r, newer, err)
			}
			err = u.Install(t.Context(), r)
			want := newBin
			if tt.errText != "" {
				want = "#!/bin/sh\n" + oldBin
				if err == nil || !strings.Contains(err.Error(), tt.errText) {
					t.Errorf("Install: %v, want an error with %q", err, tt.errText)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(target); err != nil || string(b) != want {
				t.Errorf("target file = %q, %v, want %q", b, err, want)
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("after Install: %s is not a symlink, want a symlink", link)
			}
			if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
				t.Errorf("Stat = %v, %v, want mode 0755", info, err)
			}
			entries, err := os.ReadDir(filepath.Dir(target))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("the directory has %d files, want only rexdiff", len(entries))
			}
		})
	}
}
