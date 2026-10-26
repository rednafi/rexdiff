package vcs

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/rednafi/rexdiff/internal/termtext"
)

var (
	// ErrNoPullRequest means that the branch has no pull request.
	ErrNoPullRequest = errors.New("no pull request for this branch")

	errNoRemote    = errors.New("no git remote")
	errUnsupported = errors.New("the remote is not on GitHub, GitLab, or Azure DevOps")
)

// maxComments is the number of the most recent comments that
// [Repo.PullRequest] gets. maxChecks is the number of the checks that it
// gets. The two limits are the same as in herdr-reviewr.
const (
	maxComments = 100
	maxChecks   = 100
)

// Hosts are the hosts of self-hosted forges. An empty value means only the
// public host.
type Hosts struct {
	GitHub, GitLab, Azure string
}

// PullRequest is a pull request, or a merge request on GitLab.
type PullRequest struct {
	Forge  Forge
	Number int
	Title  string
	State  PRState
	URL    string
	Author string
	Head   string // the source branch
	Base   string // the target branch
	Body   string // Markdown
	Checks []Check
	// Comments are the most recent comments, the oldest first.
	Comments []PRComment
}

// Check is a CI check.
type Check struct {
	Name   string
	Result Result
}

// Forge is the service that hosts a pull request. Its value is the name of
// the service.
type Forge string

// The forges that [Repo.PullRequest] supports.
const (
	ForgeGitHub Forge = "GitHub"
	ForgeGitLab Forge = "GitLab"
	ForgeAzure  Forge = "Azure DevOps"
)

// PRState is the state of a pull request. It is the same for each forge.
type PRState string

// The values of PRState.
const (
	PROpen   PRState = "open"
	PRDraft  PRState = "draft" // open, but not ready for review
	PRMerged PRState = "merged"
	PRClosed PRState = "closed" // closed without a merge
)

// Result is the result of a CI check. It is the same for each forge.
type Result string

// The values of Result.
const (
	ResultPass    Result = "pass"
	ResultFail    Result = "fail"    // also an error, a cancel, or a timeout
	ResultPending Result = "pending" // in the queue, or not finished
	ResultSkip    Result = "skip"    // skipped, or neutral
)

// PRComment is a comment on a pull request.
type PRComment struct {
	Author string
	When   string // the time, as the forge gives it
	Body   string // Markdown
}

// PullRequest returns the pull request of the current branch. It selects the
// forge from the host of the remote "upstream", or else "origin". Then it runs
// the command-line tool of the forge: gh, glab, or az. The tool must be
// installed and signed in. The text of the pull request has no control
// characters. If the branch has no pull request, PullRequest returns
// [ErrNoPullRequest].
func (r *Repo) PullRequest(ctx context.Context, h Hosts) (*PullRequest, error) {
	remote := r.remoteURL(ctx)
	if remote == "" {
		return nil, errNoRemote
	}
	host := remoteHost(remote)
	is := func(name string) bool { return name != "" && strings.EqualFold(host, name) }
	var pr *PullRequest
	var err error
	switch {
	case is("github.com") || is(h.GitHub):
		pr, err = r.gitHub(ctx, h.GitHub)
	case is("gitlab.com") || is(h.GitLab):
		pr, err = r.gitLab(ctx)
	case is("dev.azure.com") || is("ssh.dev.azure.com") ||
		strings.HasSuffix(host, ".visualstudio.com") || is(h.Azure):
		pr, err = r.azure(ctx)
	default:
		return nil, errUnsupported
	}
	if err != nil {
		return nil, err
	}
	if pr.Title == "" {
		return nil, ErrNoPullRequest
	}
	cleanPR(pr)
	return pr, nil
}

// remoteHost returns the host of the remote URL u, in lower case. u is a
// URL such as "ssh://git@host:22/path", or has the scp form
// "git@host:path", as herdr-reviewr reads it. A local path has no host.
func remoteHost(u string) string {
	var auth string // [user@]host[:port]
	if _, rest, ok := strings.Cut(u, "://"); ok {
		auth, _, _ = strings.Cut(rest, "/")
	} else if a, _, ok := strings.Cut(u, ":"); ok {
		auth = a
	}
	if _, h, ok := strings.CutLast(auth, "@"); ok {
		auth = h
	}
	host, _, _ := strings.Cut(auth, ":")
	return strings.ToLower(host)
}

// cleanPR removes the control characters from the text of pr, because the
// text comes from other users.
func cleanPR(pr *PullRequest) {
	texts := []*string{&pr.Title, &pr.URL, &pr.Author, &pr.Head, &pr.Base, &pr.Body}
	for i := range pr.Checks {
		texts = append(texts, &pr.Checks[i].Name)
	}
	for i := range pr.Comments {
		c := &pr.Comments[i]
		texts = append(texts, &c.Author, &c.When, &c.Body)
	}
	for _, s := range texts {
		*s = termtext.Plain(*s)
	}
}

// runJSON runs a forge tool in the root directory and decodes its JSON
// output into v. env adds to the environment. The error names the tool
// and its subcommand, for example "gh pr".
func (r *Repo) runJSON(ctx context.Context, env []string, v any, name string, args ...string) error {
	cmd, stderr := r.command(ctx, env, name, args...)
	out, err := output(ctx, cmd, stderr)
	if err == nil {
		err = json.Unmarshal([]byte(out), v)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	return nil
}

// noPR reports whether err is the error of a tool that finds no pull
// request. Such a tool exits with 1 and says so with the text msg.
func noPR(err error, msg string) bool {
	return exitedOne(err) && strings.Contains(strings.ToLower(err.Error()), msg)
}

// lastN returns the last n elements of xs, or all of them if there are
// fewer.
func lastN[T any](xs []T, n int) []T {
	return xs[max(len(xs)-n, 0):]
}

// gitHubUser is a user in the JSON output of gh.
type gitHubUser struct {
	Login string `json:"login"`
}

// gitHubPR is the JSON output of "gh pr view".
type gitHubPR struct {
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	URL         string     `json:"url"`
	Body        string     `json:"body"`
	IsDraft     bool       `json:"isDraft"`
	HeadRefName string     `json:"headRefName"`
	BaseRefName string     `json:"baseRefName"`
	Author      gitHubUser `json:"author"`
	Comments    []struct {
		Author    gitHubUser `json:"author"`
		Body      string     `json:"body"`
		CreatedAt string     `json:"createdAt"`
	} `json:"comments"`
	StatusCheckRollup []struct {
		Name       string `json:"name"`
		Context    string `json:"context"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
}

// gitHub gets the pull request with gh. A host that is not empty is the
// host of a self-hosted GitHub.
func (r *Repo) gitHub(ctx context.Context, host string) (*PullRequest, error) {
	var env []string
	if host != "" {
		env = []string{"GH_HOST=" + host}
	}
	const fields = "number,title,state,isDraft,url,body,author," +
		"headRefName,baseRefName,comments,statusCheckRollup"
	var v gitHubPR
	err := r.runJSON(ctx, env, &v, "gh", "pr", "view", "--json", fields)
	if noPR(err, "no pull requests found") {
		return nil, ErrNoPullRequest
	}
	if err != nil {
		return nil, err
	}
	pr := &PullRequest{
		Forge:  ForgeGitHub,
		Number: v.Number,
		Title:  v.Title,
		State:  gitHubState(v.State, v.IsDraft),
		URL:    v.URL,
		Author: v.Author.Login,
		Head:   v.HeadRefName,
		Base:   v.BaseRefName,
		Body:   v.Body,
	}
	// A check run has a conclusion when it is done. A status has a state.
	// As in herdr-reviewr, the checks are the first that gh gives.
	for _, c := range v.StatusCheckRollup[:min(len(v.StatusCheckRollup), maxChecks)] {
		pr.Checks = append(pr.Checks, Check{
			Name:   cmp.Or(c.Name, c.Context),
			Result: gitHubResult(cmp.Or(c.Conclusion, c.State)),
		})
	}
	for _, c := range lastN(v.Comments, maxComments) {
		pr.Comments = append(pr.Comments, PRComment{
			Author: c.Author.Login,
			When:   c.CreatedAt,
			Body:   c.Body,
		})
	}
	return pr, nil
}

// gitHubState returns the state of a GitHub pull request from its state
// and its draft flag.
func gitHubState(s string, draft bool) PRState {
	switch {
	case s == "MERGED":
		return PRMerged
	case s == "CLOSED":
		return PRClosed
	case draft:
		return PRDraft
	}
	return PROpen
}

// gitLabState returns the state of a GitLab merge request. A locked merge
// request is closed.
func gitLabState(s string) PRState {
	switch s {
	case "opened":
		return PROpen
	case "merged":
		return PRMerged
	}
	return PRClosed
}

// azureState returns the state of an Azure DevOps pull request from its
// status. An abandoned pull request is closed.
func azureState(s string) PRState {
	switch s {
	case "active":
		return PROpen
	case "completed":
		return PRMerged
	}
	return PRClosed
}

// gitHubResult returns the result of a GitHub check from its conclusion or
// its state.
func gitHubResult(s string) Result {
	switch strings.ToUpper(s) {
	case "SUCCESS":
		return ResultPass
	case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT",
		"ACTION_REQUIRED", "STARTUP_FAILURE":
		return ResultFail
	case "NEUTRAL", "SKIPPED":
		return ResultSkip
	}
	return ResultPending
}

// gitLabUser is a user in the JSON output of glab.
type gitLabUser struct {
	Username string `json:"username"`
}

// gitLabMR is the JSON output of "glab mr view".
type gitLabMR struct {
	IID         int        `json:"iid"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	WebURL      string     `json:"web_url"`
	Description string     `json:"description"`
	Source      string     `json:"source_branch"`
	Target      string     `json:"target_branch"`
	Author      gitLabUser `json:"author"`
}

// gitLabDiscussion is a thread of notes in the JSON output of
// "glab mr note list".
type gitLabDiscussion struct {
	Notes []struct {
		Body      string     `json:"body"`
		CreatedAt string     `json:"created_at"`
		System    bool       `json:"system"`
		Author    gitLabUser `json:"author"`
	} `json:"notes"`
}

// gitLab gets the merge request with glab.
func (r *Repo) gitLab(ctx context.Context) (*PullRequest, error) {
	var v gitLabMR
	err := r.runJSON(ctx, nil, &v, "glab", "mr", "view", "--output", "json")
	if noPR(err, "no open merge request") {
		return nil, ErrNoPullRequest
	}
	if err != nil {
		return nil, err
	}
	pr := &PullRequest{
		Forge:  ForgeGitLab,
		Number: v.IID,
		Title:  v.Title,
		State:  gitLabState(v.State),
		URL:    v.WebURL,
		Author: v.Author.Username,
		Head:   v.Source,
		Base:   v.Target,
		Body:   v.Description,
	}
	// The comments are optional. If glab cannot get them, return the merge
	// request without them.
	var ds []gitLabDiscussion
	args := []string{"mr", "note", "list", "--output", "json"}
	if err := r.runJSON(ctx, nil, &ds, "glab", args...); err == nil {
		pr.Comments = gitLabComments(ds)
	}
	return pr, nil
}

// gitLabComments returns the most recent notes of the discussions, the
// oldest first. It skips the notes that GitLab writes, for example "added
// 1 commit".
func gitLabComments(ds []gitLabDiscussion) []PRComment {
	var cs []PRComment
	for _, d := range ds {
		for _, n := range d.Notes {
			if n.System {
				continue
			}
			cs = append(cs, PRComment{
				Author: n.Author.Username,
				When:   n.CreatedAt,
				Body:   n.Body,
			})
		}
	}
	return lastN(cs, maxComments)
}

// azurePR is a pull request in the JSON output of "az repos pr list".
type azurePR struct {
	PullRequestID int    `json:"pullRequestId"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Description   string `json:"description"`
	SourceRefName string `json:"sourceRefName"`
	TargetRefName string `json:"targetRefName"`
	CreatedBy     struct {
		DisplayName string `json:"displayName"`
	} `json:"createdBy"`
	Repository struct {
		WebURL string `json:"webUrl"`
	} `json:"repository"`
}

// azure gets the pull request of the current branch with az.
func (r *Repo) azure(ctx context.Context) (*PullRequest, error) {
	branch, err := r.Branch(ctx)
	if err != nil {
		return nil, err
	}
	var v []azurePR
	err = r.runJSON(ctx, nil, &v, "az", "repos", "pr", "list", "--source-branch", branch,
		"--status", "all", "--top", "1", "--output", "json")
	if err != nil {
		return nil, err
	}
	if len(v) == 0 {
		return nil, ErrNoPullRequest
	}
	p := v[0]
	return &PullRequest{
		Forge:  ForgeAzure,
		Number: p.PullRequestID,
		Title:  p.Title,
		State:  azureState(p.Status),
		URL:    fmt.Sprintf("%s/pullrequest/%d", p.Repository.WebURL, p.PullRequestID),
		Author: p.CreatedBy.DisplayName,
		Head:   strings.TrimPrefix(p.SourceRefName, "refs/heads/"),
		Base:   strings.TrimPrefix(p.TargetRefName, "refs/heads/"),
		Body:   p.Description,
	}, nil
}
