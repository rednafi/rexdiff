package test

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// release returns the URL of the latest release on a fake GitHub server.
// If tampered, the checksum does not match the archive.
func release(t *testing.T, tag, bin string, tampered bool) string {
	t.Helper()
	binData, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "rexdiff", Mode: 0o755, Size: int64(len(binData)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binData); err != nil {
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
	if tampered {
		sum[0]++
	}
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

func TestUpdate(t *testing.T) {
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
	latest := release(t, "v0.2.0", next, false)
	tm := start(t, newRepo(t), opts{bin: bin, env: []string{"REXDIFF_LATEST_URL=" + latest}})
	tm.waitFor("rexdiff v0.2.0 is out: press U or click here to update")
	comment(tm, "hi")
	tm.keys("U")
	// The new rexdiff is the latest version, so it offers no update. It
	// shows the comment, but not the flash message from the old process.
	tm.waitUntil(func(s string) bool {
		return strings.Contains(s, "💬 hi") && !hasAny(s, "is out", "updating", "comment added")
	}, "show the comment in the new rexdiff")
	out, err := exec.CommandContext(t.Context(), bin, "-version").Output()
	if got := strings.TrimSpace(string(out)); err != nil || got != "v0.2.0" {
		t.Errorf("rexdiff -version = %q (%v), want v0.2.0", got, err)
	}
}

// TestUpdateBadChecksum checks that rexdiff keeps running. The tests of
// update check that the binary stays the same.
func TestUpdateBadChecksum(t *testing.T) {
	bin, err := build(t.Context(), t.TempDir(), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	latest := release(t, "v0.2.0", bin, true)
	tm := start(t, newRepo(t), opts{bin: bin, env: []string{"REXDIFF_LATEST_URL=" + latest}})
	tm.waitFor("rexdiff v0.2.0 is out")
	tm.keys("U")
	tm.waitFor("update failed", "does not match its checksum")
	tm.keys("2")
	tm.waitFor("Files (6)")
}

func headState(t *testing.T, xoff int, hidden, focus bool) string {
	t.Helper()
	h := fnv.New64a()
	_, _ = h.Write([]byte(numbered("line", 1, 40))) // the committed content of long.txt; Write on a hash.Hash never fails
	state := fmt.Sprintf(`{"tab": 0, "scope": "uncommitted", "base": "", "from": "", "to": "",
		"turn": null, "change_path": "long.txt", "file_path": "",
		"changes": {"cur": 3, "top": 0, "xoff": %d}, "viewer": {"cur": 0, "top": 0, "xoff": 0},
		"pr": {"cur": 0, "top": 0, "xoff": 0}, "split": false, "wrap": false, "rendered": false,
		"list_pos": "left", "list_hidden": %t, "focus_list": %t, "list_w": 50,
		"folds": {"long.txt": {"old": %d, "ranges": [[20, 20]]}},
		"comments": {"last": 1, "items": [{"id": 1, "path": "long.txt", "side": 1,
			"start": 20, "end": 20, "text": "kept", "in_diff": true, "rev": "",
			"code": ["line 20"], "snippet": null}]},
		"last_sent": "", "beside": ""}`, xoff, hidden, focus, h.Sum64())
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResumeState(t *testing.T) {
	t.Run("shown", func(t *testing.T) {
		path := headState(t, 0, false, true)
		tm := start(t, newRepo(t), opts{width: 160, env: []string{"REXDIFF_RESUME=" + path}})
		s := tm.waitFor("long.txt  +2 -2", "line 20", "💬 kept", "●1")
		if strings.Contains(s, "unmodified lines") {
			t.Errorf("the fold of the state is not expanded. The screen shows:\n%s", s)
		}
		if c := column(s, "Changes (2)"); c < 0 || c > 5 {
			t.Errorf("the file list starts at column %d, want it at the left", c)
		}
		if ls := lines(s, "M a.go"); len(ls) == 0 || column(ls[0], "│") < 48 || column(ls[0], "│") > 52 {
			t.Errorf("the file list rows %q, want a width of 50", ls)
		}
		tm.waitCursor("line 2") // row 3, after the header and the first change
		tm.keys("k")
		tm.waitFor("a.go  +2 -1")
	})
	t.Run("scrolled and hidden", func(t *testing.T) {
		path := headState(t, 3, true, false)
		tm := start(t, newRepo(t), opts{width: 160, env: []string{"REXDIFF_RESUME=" + path}})
		s := tm.waitFor("long.txt  +2 -2", "e 20", "💬 kept")
		if hasAny(s, "Changes (2)", "line 20") {
			t.Errorf("the file list shows, or the lines do not scroll sideways. The screen shows:\n%s", s)
		}
	})
}
