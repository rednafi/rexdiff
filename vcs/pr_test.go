package vcs_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/vcs"
)

// fakeTool prints calls[args], or fails with "unexpected call".
func fakeTool(t *testing.T, dir, name string, calls map[string]string) {
	t.Helper()
	var script strings.Builder
	script.WriteString("case \"$*\" in\n")
	for args, out := range calls {
		fmt.Fprintf(&script, "'%s') cat <<'EOF'\n%s\nEOF\n;;\n", args, out)
	}
	script.WriteString("*) echo unexpected call: \"$*\" >&2; exit 1 ;;\nesac\n")
	testgit.Script(t, dir, name, script.String())
}

func repoAt(t *testing.T, env []string, remote string) (*vcs.Repo, string) {
	t.Helper()
	r, dir := openTestRepoEnv(t, env)
	testgit.Git(t, dir, "remote", "add", "origin", remote)
	return r, dir
}

const ghArgs = "pr view --json number,title,state,isDraft,url,body,author," +
	"headRefName,baseRefName,comments,statusCheckRollup"

func azArgs(branch string) string {
	return "repos pr list --source-branch " + branch + " --status all --top 1 --output json"
}

func TestPR(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := testgit.ToolEnv(t, dir)
	fakeTool(t, dir, "gh", map[string]string{ghArgs: `{
		"number": 7, "title": "Add x", "state": "OPEN", "isDraft": true,
		"url": "https://github.com/o/r/pull/7",
		"body": "Body", "author": {"login": "ann"},
		"headRefName": "feat", "baseRefName": "main",
		"comments": [
			{"author": {"login": "bob"}, "body": "LGTM",
				"createdAt": "2026-01-02T03:04:05Z"}],
		"statusCheckRollup": [
			{"name": "test", "conclusion": "SUCCESS"},
			{"context": "ci/lint", "state": "PENDING"},
			{"name": "build", "conclusion": "STARTUP_FAILURE"},
			{"context": "ci/deploy", "state": "ERROR"},
			{"name": "docs", "conclusion": "NEUTRAL"},
			{"name": "e2e", "status": "IN_PROGRESS"}]}`})
	fakeTool(t, dir, "glab", map[string]string{
		"mr view --output json": `{"iid": 3, "title": "Fix y", "state": "opened",
		"web_url": "https://gitlab.com/o/r/-/merge_requests/3", "description": "D",
		"source_branch": "fix", "target_branch": "main", "author": {"username": "cy"}}`,
		"mr note list --output json": `[
		{"id": "d1", "notes": [
			{"body": "added 1 commit", "system": true, "author": {"username": "cy"}}]},
		{"id": "d2", "notes": [
			{"body": "Why?", "created_at": "2026-01-02T03:04:05Z",
				"author": {"username": "eve"}},
			{"body": "Speed.", "created_at": "2026-01-03T03:04:05Z",
				"author": {"username": "cy"}}]}]`,
	})
	fakeTool(t, dir, "az", map[string]string{azArgs("feat"): `[{
		"pullRequestId": 9, "title": "Z", "status": "active", "description": "",
		"sourceRefName": "refs/heads/feat", "targetRefName": "refs/heads/main",
		"createdBy": {"displayName": "Dee"},
		"repository": {"webUrl": "https://dev.azure.com/o/p/_git/r"}}]`})

	tests := []struct {
		name   string
		remote string
		hosts  vcs.Hosts
		want   *vcs.PR
	}{
		{"github ssh", "git@github.com:o/r.git", vcs.Hosts{}, &vcs.PR{
			Forge:  vcs.ForgeGitHub,
			Number: 7,
			Title:  "Add x",
			State:  vcs.PRDraft,
			URL:    "https://github.com/o/r/pull/7",
			Author: "ann",
			Head:   "feat",
			Base:   "main",
			Body:   "Body",
			Checks: []vcs.Check{
				{Name: "test", Result: vcs.ResultPass},
				{Name: "ci/lint", Result: vcs.ResultPending},
				{Name: "build", Result: vcs.ResultFail},
				{Name: "ci/deploy", Result: vcs.ResultFail},
				{Name: "docs", Result: vcs.ResultSkip},
				{Name: "e2e", Result: vcs.ResultPending},
			},
			Tally: vcs.Tally{Pass: 1, Fail: 2, Pending: 2, Skip: 1},
			Comments: []vcs.PRComment{
				{Author: "bob", When: "2026-01-02T03:04:05Z", Body: "LGTM"},
			},
		}},
		{"gitlab custom host", "https://Git.Corp/o/r", vcs.Hosts{GitLab: "git.CORP"}, &vcs.PR{
			Forge:  vcs.ForgeGitLab,
			Number: 3,
			Title:  "Fix y",
			State:  vcs.PROpen,
			URL:    "https://gitlab.com/o/r/-/merge_requests/3",
			Author: "cy",
			Head:   "fix",
			Base:   "main",
			Body:   "D",
			Comments: []vcs.PRComment{
				{Author: "eve", When: "2026-01-02T03:04:05Z", Body: "Why?"},
				{Author: "cy", When: "2026-01-03T03:04:05Z", Body: "Speed."},
			},
		}},
		{"azure", "https://dev.azure.com/o/p/_git/r", vcs.Hosts{}, &vcs.PR{
			Forge:  vcs.ForgeAzure,
			Number: 9,
			Title:  "Z",
			State:  vcs.PROpen,
			URL:    "https://dev.azure.com/o/p/_git/r/pullrequest/9",
			Author: "Dee",
			Head:   "feat",
			Base:   "main",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, _ := repoAt(t, env, tt.remote)
			pr, err := r.PR(t.Context(), tt.hosts)
			if err != nil {
				t.Fatalf("PR = %v, want no error", err)
			}
			if !reflect.DeepEqual(pr, tt.want) {
				t.Errorf("PR = \n%+v\nwant:\n%+v", pr, tt.want)
			}
		})
	}
}

func TestPRErrors(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	env := testgit.ToolEnv(t, bin)
	fakeTool(t, bin, "az", map[string]string{azArgs("none"): `[]`})
	const azure = "https://dev.azure.com/o/p/_git/r"
	const unsupported = "the remote is not on GitHub, GitLab, or Azure DevOps"
	corp := vcs.Hosts{GitLab: "Git.Corp"}
	tests := []struct {
		name           string
		remote, branch string
		hosts          vcs.Hosts
		wantErr        error  // if not nil, err must wrap it
		errText        string // if not empty, err must contain it
	}{
		// Only the host chooses the forge, not a forge name in the path.
		{
			name:    "github in the path",
			remote:  "https://example.com/github.com/r.git",
			branch:  "feat",
			errText: unsupported,
		},
		{
			name:    "gitlab in the path",
			remote:  "git@example.com:gitlab/r.git",
			branch:  "feat",
			errText: unsupported,
		},
		{
			name:    "other corp host",
			remote:  "https://git.corp.example/r",
			branch:  "feat",
			hosts:   corp,
			errText: unsupported,
		},
		{
			name:    "gitlab custom host",
			remote:  "git@git.corp:o/r.git",
			branch:  "feat",
			hosts:   corp,
			errText: "glab is not installed",
		},
		{
			name:    "github ssh with port",
			remote:  "ssh://git@GitHub.com:22/o/r",
			branch:  "feat",
			errText: "gh is not installed",
		},
		{
			name:    "visualstudio other branch",
			remote:  "https://u@org.visualstudio.com/p/_git/r",
			branch:  "other",
			errText: "unexpected call: " + azArgs("other"),
		},
		{name: "no remote", branch: "feat", errText: "no git remote"},
		{name: "azure no pull request", remote: azure, branch: "none", wantErr: vcs.ErrNoPR},
		{
			name:    "azure other branch",
			remote:  azure,
			branch:  "other",
			errText: "unexpected call: " + azArgs("other"),
		},
		{
			name:    "github with no gh",
			remote:  "https://github.com/o/r",
			branch:  "feat",
			errText: "gh is not installed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, dir := openTestRepoEnv(t, env)
			if tt.remote != "" {
				testgit.Git(t, dir, "remote", "add", "origin", tt.remote)
			}
			if tt.branch != "feat" {
				testgit.Git(t, dir, "checkout", "-qb", tt.branch)
			}
			_, err := r.PR(t.Context(), tt.hosts)
			switch {
			case err == nil:
				t.Error("PR: no error, want an error")
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Errorf("PR: err = %v, want %v", err, tt.wantErr)
			case tt.errText != "" && !strings.Contains(err.Error(), tt.errText):
				t.Errorf("PR: err = %q, want %q in it", err, tt.errText)
			}
		})
	}
}

// TestPRHostile uses an escape sequence that sets the terminal title.
func TestPRHostile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const osc = `\u001b]0;owned\u0007`
	fakeTool(t, dir, "gh", map[string]string{ghArgs: `{
		"number": 7, "title": "Add` + osc + `", "state": "OPEN",
		"body": "a` + osc + `\nb", "author": {"login": "ann` + osc + `"},
		"comments": [{"author": {"login": "bob"}, "body": "x` + osc + `"}],
		"statusCheckRollup": [{"name": "t` + osc + `", "conclusion": "SUCCESS"}]}`})
	r, _ := repoAt(t, testgit.ToolEnv(t, dir), "https://github.com/o/r")
	pr, err := r.PR(t.Context(), vcs.Hosts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Comments) != 1 || len(pr.Checks) != 1 {
		t.Fatalf("PR has %d comments and %d checks, want 1 and 1",
			len(pr.Comments), len(pr.Checks))
	}
	got := []string{pr.Title, pr.Body, pr.Author, pr.Comments[0].Body, pr.Checks[0].Name}
	want := []string{"Add]0;owned", "a]0;owned\nb", "ann]0;owned", "x]0;owned", "t]0;owned"}
	if !slices.Equal(got, want) {
		t.Errorf("PR texts = %q, want %q", got, want)
	}
}

func TestPRCheckLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		pass, fail int
		wantTally  vcs.Tally
		wantFailed int
		wantLast   string
	}{
		{"101 passing", 101, 0, vcs.Tally{Pass: 101}, 0, "c99"},
		{"100 passing then 50 failing", 100, 50, vcs.Tally{Pass: 100, Fail: 50}, 50, "c149"},
		{"10 passing then 150 failing", 10, 150, vcs.Tally{Pass: 10, Fail: 150}, 100, "c109"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var checks []string
			for i := range tt.pass + tt.fail {
				conclusion := "SUCCESS"
				if i >= tt.pass {
					conclusion = "FAILURE"
				}
				checks = append(checks, fmt.Sprintf(`{"name": "c%d", "conclusion": %q}`, i, conclusion))
			}
			fakeTool(t, dir, "gh", map[string]string{ghArgs: `{"number": 7, "title": "T",
				"statusCheckRollup": [` + strings.Join(checks, ",") + `]}`})
			r, _ := repoAt(t, testgit.ToolEnv(t, dir), "https://github.com/o/r")
			pr, err := r.PR(t.Context(), vcs.Hosts{})
			if err != nil {
				t.Fatal(err)
			}
			if pr.Tally != tt.wantTally {
				t.Errorf("Tally = %+v, want %+v", pr.Tally, tt.wantTally)
			}
			n := len(pr.Checks)
			if n != 100 || pr.Checks[n-1].Name != tt.wantLast {
				t.Fatalf("PR has %d checks %+v, want 100, the last %s",
					n, pr.Checks[max(n-3, 0):], tt.wantLast)
			}
			failed, prev := 0, -1
			for _, c := range pr.Checks {
				if c.Result == vcs.ResultFail {
					failed++
				}
				cur, _ := strconv.Atoi(strings.TrimPrefix(c.Name, "c"))
				if cur <= prev {
					t.Fatalf("check c%d comes after c%d, want the order gh gives", cur, prev)
				}
				prev = cur
			}
			if failed != tt.wantFailed {
				t.Errorf("PR keeps %d failed checks, want %d", failed, tt.wantFailed)
			}
		})
	}
}

func TestNoPR(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := testgit.ToolEnv(t, dir)
	testgit.Script(t, dir, "gh", "echo 'no pull requests found for branch \"feat\"' >&2\nexit 1\n")
	testgit.Script(t, dir, "glab", "echo 'no open merge request available for \"feat\"' >&2\nexit 1\n")
	for _, remote := range []string{"https://github.com/o/r", "https://gitlab.com/o/r"} {
		r, _ := repoAt(t, env, remote)
		if _, err := r.PR(t.Context(), vcs.Hosts{}); !errors.Is(err, vcs.ErrNoPR) {
			t.Errorf("PR(%s) = %v, want ErrNoPR", remote, err)
		}
	}
}
