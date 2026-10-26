package test

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// release serves the latest release tag with the binary at bin, as GitHub
// does. It returns the address of the latest release.
func release(t *testing.T, tag, bin string) string {
	t.Helper()
	content, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "rexdiff", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	name := "rexdiff-" + tag + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	archive := b.Bytes()
	sum := sha256.Sum256(archive)
	files := fstest.MapFS{
		name:             {Data: archive},
		"sha256sums.txt": {Data: []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")},
	}
	srv := httptest.NewServer(http.FileServerFS(files))
	t.Cleanup(srv.Close)
	files["latest"] = &fstest.MapFile{Data: []byte(`{"tag_name": "` + tag + `", "assets": [` +
		`{"name": "` + name + `", "browser_download_url": "` + srv.URL + "/" + name + `"},` +
		`{"name": "sha256sums.txt", "browser_download_url": "` + srv.URL + `/sha256sums.txt"}]}`)}
	return srv.URL + "/latest"
}

// TestUpdate checks that U installs the latest release and starts it in
// the same terminal, with the comments and the place of the user.
func TestUpdate(t *testing.T) {
	// The two builds run at once.
	var next string
	var nextErr error
	var wg sync.WaitGroup
	nextDir := t.TempDir()
	wg.Go(func() { next, nextErr = build(t.Context(), nextDir, "0.2.0") })
	bin, err := build(t.Context(), t.TempDir(), "0.1.0")
	wg.Wait()
	if err = cmp.Or(err, nextErr); err != nil {
		t.Fatal(err)
	}
	latest := release(t, "v0.2.0", next)
	tm := start(t, newRepo(t), opts{bin: bin, env: []string{"REXDIFF_LATEST_URL=" + latest}})
	tm.waitFor("rexdiff v0.2.0 is out: press U or click here to update")
	comment(tm, "hi")
	tm.keys("U")
	// The new rexdiff is the latest, so it shows no update. It has the
	// comment, but not the message of the old one.
	tm.waitUntil(func(s string) bool {
		return strings.Contains(s, "💬 hi") && !hasAny(s, "is out", "updating", "comment added")
	}, "show the comment in the new rexdiff")
	out, err := exec.CommandContext(t.Context(), bin, "-version").Output()
	if got := strings.TrimSpace(string(out)); err != nil || got != "v0.2.0" {
		t.Errorf("rexdiff -version = %q (%v), want v0.2.0", got, err)
	}
}
