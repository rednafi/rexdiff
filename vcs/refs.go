package vcs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rednafi/rexdiff/internal/proc"
)

// Commit is a commit in the history of HEAD.
type Commit struct {
	SHA, Short string
	Subject    string
	Author     string
	When       string // relative, for example "2 hours ago"
}

// Limits on the number of commits that [Repo.Commits] returns.
const (
	LastCommits = 50
	MaxCommits  = 1000
)

// Commits returns the first-parent commits of HEAD after since, newest
// first. If since is "", it returns the last [LastCommits] commits.
func (r *Repo) Commits(ctx context.Context, since string) ([]Commit, error) {
	// The subject comes last, because it can contain the separator.
	const (
		format  = "--format=%H%x1f%h%x1f%ar%x1f%an%x1f%s"
		nFields = 5
	)
	_, err := r.run(ctx, nil, "rev-parse", "-q", "--verify", "HEAD")
	if isExitOne(err) {
		return nil, nil
	}
	args := []string{"log", "--first-parent", format}
	if since == "" {
		args = append(args, "-n", strconv.Itoa(LastCommits), "--end-of-options", "HEAD")
	} else {
		args = append(args, "-n", strconv.Itoa(MaxCommits), "--end-of-options", since+"..HEAD")
	}
	out, err := r.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var cs []Commit
	for l := range strings.SplitSeq(out, "\n") {
		f := strings.SplitN(l, "\x1f", nFields)
		if len(f) != nFields {
			continue
		}
		cs = append(cs, Commit{
			SHA:     f[0],
			Short:   f[1],
			When:    f[2],
			Author:  f[3],
			Subject: f[4],
		})
	}
	return cs, nil
}

// Branches returns the branches, newest first. origin/x hides the local x.
func (r *Repo) Branches(ctx context.Context) ([]string, error) {
	out, err := r.run(ctx, nil, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)", "refs/remotes/origin", "refs/heads")
	if err != nil {
		return nil, err
	}
	refs := strings.Fields(out) // a ref name cannot contain a space
	onOrigin := map[string]bool{}
	for _, ref := range refs {
		if name, ok := strings.CutPrefix(ref, "refs/remotes/origin/"); ok {
			onOrigin[name] = true
		}
	}
	var bs []string
	for _, ref := range refs {
		local, isLocal := strings.CutPrefix(ref, "refs/heads/")
		remote := strings.TrimPrefix(ref, "refs/remotes/")
		switch {
		case isLocal && !onOrigin[local]:
			bs = append(bs, local)
		case !isLocal && remote != "origin/HEAD":
			bs = append(bs, remote)
		}
	}
	return bs, nil
}

// DefaultBase returns the default branch, or "HEAD".
func (r *Repo) DefaultBase(ctx context.Context) string {
	if ref, err := r.run(ctx, nil, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		// git fetch --prune can delete the target of the symbolic ref.
		ref = strings.TrimSpace(ref)
		if _, err := r.ResolveRev(ctx, ref); err == nil {
			return ref
		}
	}
	for _, b := range []string{"main", "master", "trunk", "origin/main", "origin/master"} {
		if _, err := r.ResolveRev(ctx, b); err == nil {
			return b
		}
	}
	return "HEAD"
}

// Branch returns the branch that HEAD points to, or "HEAD" if detached.
func (r *Repo) Branch(ctx context.Context) (string, error) {
	out, err := r.run(ctx, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if isExitOne(err) {
		return "HEAD", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Repo) remoteURL(ctx context.Context) string {
	for _, name := range []string{"upstream", "origin"} {
		if u, err := r.run(ctx, nil, "remote", "get-url", name); err == nil {
			return strings.TrimSpace(u)
		}
	}
	return ""
}

// GitEditor returns core.editor, or "" if it is not set.
func (r *Repo) GitEditor(ctx context.Context) string {
	out, _ := r.run(ctx, nil, "config", "core.editor")
	return strings.TrimSpace(out)
}

// Entry is a path in the working tree.
type Entry struct {
	Path string
	// An ignored directory is one entry whose path ends with a slash.
	Ignored bool
}

// --deduplicate lists a file with a merge conflict once, not once per stage.
var lsFilesArgs = []string{"-co", "--deduplicate", "--exclude-standard"}

// Files returns the tracked, untracked and ignored paths, sorted.
func (r *Repo) Files(ctx context.Context) ([]Entry, error) {
	gone, err := r.lsFiles(ctx, "-d")
	if err != nil {
		return nil, err
	}
	deleted := make(map[string]bool, len(gone))
	for _, p := range gone {
		deleted[p] = true
	}
	lists := []struct {
		args    []string
		ignored bool
	}{
		{lsFilesArgs, false},
		{[]string{"-oi", "--exclude-standard", "--directory"}, true},
	}
	var es []Entry
	for _, l := range lists {
		ps, err := r.lsFiles(ctx, l.args...)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if !l.ignored && deleted[p] {
				continue
			}
			es = append(es, Entry{Path: p, Ignored: l.ignored})
		}
	}
	slices.SortStableFunc(es, func(a, b Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return es, nil
}

// IgnoredDir reads an ignored directory that git lists as one path.
func (r *Repo) IgnoredDir(dir string) []Entry {
	des, err := os.ReadDir(filepath.Join(r.root, filepath.FromSlash(dir)))
	if err != nil {
		return nil
	}
	es := make([]Entry, 0, len(des))
	for _, de := range des {
		p := dir + "/" + de.Name()
		if de.IsDir() {
			p += "/"
		}
		es = append(es, Entry{Path: p, Ignored: true})
	}
	return es
}

// Match is a line that [Repo.Grep] found.
type Match struct {
	Path string
	Line int    // 1-based
	Text string // trimmed, at most 512 bytes
}

const maxGrepLineBytes = 512

// Grep returns up to limit lines that contain the literal text q. After
// budget, it stops at the next match. more is true if it stopped early.
func (r *Repo) Grep(ctx context.Context, q string, limit int, budget time.Duration) (ms []Match, more bool, err error) {
	if strings.Contains(q, "\n") {
		return nil, false, nil
	}
	// --no-column overrides grep.column, which adds a field to each match.
	args := []string{"grep", "-n", "--no-column", "-z", "-I", "-F", "--untracked", "--no-color"}
	if !strings.ContainsFunc(q, unicode.IsUpper) {
		args = append(args, "-i")
	}
	cmd, stderr := r.command(ctx, nil, "git", append(args, "-e", q)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		if isPastDeadline(ctx) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("git grep: %w", proc.Error(ctx, "git", err, nil))
	}
	var found, late, killed atomic.Bool
	timer := time.AfterFunc(budget, func() {
		late.Store(true)
		if found.Load() {
			killed.Store(true)
			_ = cmd.Process.Kill()
		}
	})
	ms, more = readMatches(bufio.NewReader(out), limit, func() bool {
		found.Store(true)
		return late.Load()
	})
	timer.Stop()
	more = more || killed.Load()
	if more {
		_ = cmd.Process.Kill()
	}
	err = cmd.Wait()
	switch {
	case err == nil, more, isExitOne(err): // finished, stopped early, or no match
		return ms, more, nil
	case isPastDeadline(ctx):
		return ms, true, nil
	case ctx.Err() != nil:
		return nil, false, context.Cause(ctx)
	}
	return nil, false, fmt.Errorf("git grep: %w", proc.Error(ctx, "git", err, stderr.Bytes()))
}

func isPastDeadline(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// readMatches reads "path NUL line NUL text LF" records. Only the text ends
// with LF, because a path can contain one.
func readMatches(r *bufio.Reader, limit int, stop func() bool) (ms []Match, more bool) {
	for len(ms) < limit {
		path, err := r.ReadString(0)
		if err != nil {
			return ms, false
		}
		// Stopping git can cut off the last record. It is not a match.
		num, err := r.ReadString(0)
		if err != nil {
			return ms, true
		}
		text, err := readStart(r, maxGrepLineBytes)
		if err != nil {
			return ms, true
		}
		n, _ := strconv.Atoi(strings.TrimSuffix(num, "\x00"))
		ms = append(ms, Match{
			Path: strings.TrimSuffix(path, "\x00"),
			Line: n,
			Text: strings.TrimSpace(text),
		})
		if stop() {
			return ms, true
		}
	}
	_, err := r.Peek(1)
	return ms, err == nil
}

// readStart returns at most n bytes of the line, without a cut rune.
func readStart(r *bufio.Reader, n int) (string, error) {
	var b []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(b)+len(chunk) > n {
			b = append(b, chunk[:n-len(b)]...)
			k := len(b) - 1
			for k > 0 && len(b)-k < utf8.UTFMax && !utf8.RuneStart(b[k]) {
				k--
			}
			if k >= 0 && !utf8.FullRune(b[k:]) {
				b = b[:k]
			}
			n = len(b)
		} else {
			b = append(b, chunk...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return string(b), err
		}
	}
}

// Refs under refs/worktree/ are separate for each worktree.
const (
	baseRef = "refs/worktree/rexdiff/base-pick"
	turnRef = "refs/worktree/rexdiff/turn-base"
)

// SavedBase returns the base that [Repo.SaveBase] saved, or "" if invalid.
func (r *Repo) SavedBase(ctx context.Context) string {
	out, err := r.run(ctx, nil, "cat-file", "blob", baseRef)
	name := strings.TrimSpace(out)
	if err != nil || !isRefName(name) {
		return ""
	}
	return name
}

// isRefName rejects spaces, control characters and a leading "-", which
// git can read as an option.
func isRefName(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") &&
		!strings.ContainsFunc(s, func(c rune) bool { return c <= ' ' || c == 0x7f })
}

// SaveBase saves name as the base for this worktree. For the default
// branch, it deletes the saved base.
func (r *Repo) SaveBase(ctx context.Context, name string) error {
	if name == r.DefaultBase(ctx) {
		return r.deleteRef(ctx, baseRef)
	}
	if !isRefName(name) {
		return fmt.Errorf("bad base %q", name)
	}
	blobID, err := r.runInput(ctx, nil, strings.NewReader(name),
		"hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	_, err = r.run(ctx, nil, "update-ref", baseRef, strings.TrimSpace(blobID))
	return err
}

// SavedTurn returns the tree that [Repo.SaveTurn] saved, or "".
func (r *Repo) SavedTurn(ctx context.Context) string {
	tree, err := r.ResolveRev(ctx, turnRef)
	if err != nil {
		return ""
	}
	return tree
}

// SaveTurn saves tree as the state at the start of the agent's last turn.
// The ref stops git gc from deleting the tree. "" deletes the saved tree.
func (r *Repo) SaveTurn(ctx context.Context, tree string) error {
	if tree == "" {
		return r.deleteRef(ctx, turnRef)
	}
	_, err := r.run(ctx, nil, "update-ref", turnRef, tree)
	return err
}

func (r *Repo) deleteRef(ctx context.Context, ref string) error {
	_, err := r.run(ctx, nil, "update-ref", "-d", ref)
	return err
}
