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
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/update"
)

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// newInstaller puts env after its defaults, so env can override them.
func newInstaller(t *testing.T, latest, bin, stamped string, env ...string) *update.Installer {
	t.Helper()
	env = slices.Concat([]string{"REXDIFF_LATEST_URL=" + latest, "PATH=/usr/bin:/bin"}, env)
	u, ok := update.NewInstaller(env, bin, stamped)
	if !ok {
		t.Fatalf("NewInstaller(%q, %s, %s) = nil, false, want an Installer", env, bin, stamped)
	}
	return u
}

func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

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
		if u, ok := update.NewInstaller(nil, tt.bin, tt.stamped); u != nil || ok {
			t.Errorf("NewInstaller(nil, %q, %q) = %+v, %t, want nil, false", tt.bin, tt.stamped, u, ok)
		}
	}
}

func TestLatest(t *testing.T) {
	t.Parallel()
	release := func(tag string) string {
		return `{"tag_name": "` + tag + `", "assets": [], "other": 1}`
	}
	tests := []struct {
		name, have, body string // an empty body makes the server return 404
		tag              string
		newer            bool
		errText          string
	}{
		{name: "minor", have: "0.1.0", body: release("v0.2.0"), tag: "v0.2.0", newer: true},
		{name: "patch", have: "v0.1.0", body: release("v0.1.1"), tag: "v0.1.1", newer: true},
		{name: "major", have: "0.9.9", body: release("v1.0.0"), tag: "v1.0.0", newer: true},
		{name: "two digits", have: "0.9.0", body: release("v0.10.0"), tag: "v0.10.0", newer: true},
		// GoReleaser puts the v of the tag in the archive names. Thus a tag
		// without a v is never newer.
		{name: "no v", have: "0.1.0", body: release("0.2.0"), tag: "0.2.0"},
		{name: "same", have: "0.1.0", body: release("v0.1.0"), tag: "v0.1.0"},
		{name: "older", have: "0.2.0", body: release("v0.1.0"), tag: "v0.1.0"},
		{name: "release candidate", have: "0.1.0", body: release("v0.2.0-rc1"), tag: "v0.2.0-rc1"},
		{name: "no tag", have: "0.1.0", body: release("")},
		{name: "sign", have: "0.9.0", body: release("v+1.0.0"), tag: "v+1.0.0"},
		{name: "minus", have: "0.9.0", body: release("v1.-0.0"), tag: "v1.-0.0"},
		{name: "status", have: "0.1.0", errText: "404"},
		{name: "bad JSON", have: "0.1.0", body: "not json", errText: "read the latest release"},
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
			u := newInstaller(t, fmt.Sprint(base, "/", i), "/bin/rexdiff", tt.have)
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

func TestInstallBrew(t *testing.T) {
	t.Parallel()
	// Install must prefer the brew next to the Caskroom, because Rex starts
	// rexdiff with a PATH that does not include Homebrew.
	const logBrew = `echo "hints=$HOMEBREW_NO_ENV_HINTS ` +
		`ni=$NONINTERACTIVE auto=$HOMEBREW_NO_AUTO_UPDATE ${0##*/} $*" ` +
		`>> "${0%/*}/../calls"` + "\n"
	const busy = "echo Updating Homebrew...\necho 'Error: another brew is running'\nexit 1\n"
	const calls = "hints=1 ni=1 auto= brew update --quiet\n" +
		"hints=1 ni=1 auto=1 brew upgrade --cask rexdiff\n"
	tests := []struct {
		name    string
		beside  bool // the brew is next to the Caskroom, not only in PATH
		brew    string
		version string // the version that the new rexdiff reports
		cancel  bool
		calls   string // "" skips the check
		errText string
	}{
		{name: "beside the Caskroom", beside: true, brew: logBrew, version: "v0.2.0", calls: calls},
		{name: "in PATH", brew: logBrew, version: "v0.2.0", calls: calls},
		{
			name: "brew fails", beside: true, brew: busy, version: "v0.2.0",
			errText: "brew: Error: another brew is running: exit status 1",
		},
		{
			name: "brew fails with no output", beside: true, brew: "exit 1\n", version: "v0.2.0",
			errText: "brew: exit status 1",
		},
		{
			name: "old version", beside: true, brew: logBrew, version: "v0.1.0",
			errText: "the update left rexdiff v0.1.0, not v0.2.0",
		},
		// A cancel does not stop brew, because a half install can break
		// the cask.
		{name: "outlives a cancel", beside: true, brew: logBrew, version: "v0.2.0", cancel: true, calls: calls},
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
			u := newInstaller(t, "http://127.0.0.1:1/latest", link, "0.1.0", pathEnv)
			err := u.Install(ctx, update.Release{Tag: "v0.2.0"})
			switch {
			case tt.errText == "" && err != nil:
				t.Errorf("Install = %v, want nil", err)
			case tt.errText != "" && (err == nil || !strings.Contains(err.Error(), tt.errText)):
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
	big := archive(t, strings.Repeat("x", 128<<20+1))
	files := fstest.MapFS{}
	for dir, a := range map[string][]byte{"good": good, "stale": stale, "big": big, "bad": good} {
		files[dir+"/"+name] = &fstest.MapFile{Data: a}
		files[dir+"/sha256sums.txt"] = &fstest.MapFile{Data: []byte(sums(a))}
	}
	files["bad/"+name] = &fstest.MapFile{Data: []byte("tampered")}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(files))
	// The directory "none" does not exist, so its release has no assets.
	mux.HandleFunc("GET /{dir}/latest", func(w http.ResponseWriter, req *http.Request) {
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		dir := req.PathValue("dir")
		assets := []asset{}
		for _, f := range []string{name, "sha256sums.txt"} {
			if _, ok := files[dir+"/"+f]; ok {
				assets = append(assets, asset{Name: f, URL: "http://" + req.Host + "/" + dir + "/" + f})
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
		errText   string
	}{
		{name: "good", dir: "good"},
		{name: "tampered", dir: "bad", errText: "does not match its checksum"},
		{name: "other version", dir: "stale", errText: "the update left rexdiff v0.1.9, not v0.2.0"},
		{name: "too large", dir: "big", errText: "larger than"},
		{name: "no archive", dir: "none", errText: "has no " + name},
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
			u := newInstaller(t, base+"/"+tt.dir+"/latest", link, "0.1.0")
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
