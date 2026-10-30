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
	// ErrNoPR means that the current branch has no pull request.
	ErrNoPR = errors.New("no pull request for this branch")

	errNoRemote    = errors.New("no git remote")
	errUnsupported = errors.New("the remote is not on GitHub, GitLab, or Azure DevOps")
)

const (
	maxComments = 100
	maxChecks   = 100
)

// Hosts names self-hosted forges. An empty field means the public host.
type Hosts struct {
	GitHub, GitLab, Azure string
}

// PR is a pull request, or a merge request on GitLab.
type PR struct {
	Forge  Forge
	Number int
	Title  string
	State  PRState
	URL    string
	Author string
	Head   string // the source branch
	Base   string // the target branch
	Body   string // Markdown
	// Checks holds at most 100 checks, failed ones first. Tally counts all.
	Checks   []Check
	Tally    Tally
	Comments []PRComment // the newest 100, oldest first
}

// Check is a CI check.
type Check struct {
	Name   string
	Result Result
}

// Tally counts the CI checks by result.
type Tally struct {
	Pass, Fail, Pending, Skip int
}

// Total returns the number of checks.
func (t *Tally) Total() int {
	return t.Pass + t.Fail + t.Pending + t.Skip
}

func (t *Tally) add(res Result) {
	switch res {
	case ResultPass:
		t.Pass++
	case ResultFail:
		t.Fail++
	case ResultSkip:
		t.Skip++
	default:
		t.Pending++
	}
}

// Forge is the display name of the service that hosts a pull request.
type Forge string

// The forges that [Repo.PR] supports.
const (
	ForgeGitHub Forge = "GitHub"
	ForgeGitLab Forge = "GitLab"
	ForgeAzure  Forge = "Azure DevOps"
)

// PRState is the state of a pull request.
type PRState string

// The values of PRState.
const (
	PROpen   PRState = "open"
	PRDraft  PRState = "draft" // open, but not ready for review
	PRMerged PRState = "merged"
	PRClosed PRState = "closed" // closed without being merged
)

// Result is the result of a CI check.
type Result string

// The values of Result.
const (
	ResultPass    Result = "pass"
	ResultFail    Result = "fail"    // also an error, a cancellation or a timeout
	ResultPending Result = "pending" // queued or still running
	ResultSkip    Result = "skip"    // skipped, or neutral
)

// PRComment is a comment on a pull request.
type PRComment struct {
	Author string
	When   string // creation time, as the forge formats it
	Body   string // Markdown
}

// PR returns the pull request for the current branch with gh, glab or az.
func (r *Repo) PR(ctx context.Context, h Hosts) (*PR, error) {
	remote := r.remoteURL(ctx)
	if remote == "" {
		return nil, errNoRemote
	}
	host := remoteHost(remote)
	is := func(name string) bool { return name != "" && strings.EqualFold(host, name) }
	var pr *PR
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
		return nil, ErrNoPR
	}
	sanitizePR(pr)
	return pr, nil
}

// remoteHost also accepts the scp-like form "git@host:path".
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

// sanitizePR removes control characters, because others write the text.
func sanitizePR(pr *PR) {
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

func isNoPR(err error, msg string) bool {
	// gh and glab exit 1 for every failure, so check the message.
	return isExitOne(err) && strings.Contains(strings.ToLower(err.Error()), msg)
}

func lastN[T any](xs []T, n int) []T {
	return xs[max(len(xs)-n, 0):]
}

type gitHubUser struct {
	Login string `json:"login"`
}

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

func (r *Repo) gitHub(ctx context.Context, host string) (*PR, error) {
	var env []string
	if host != "" {
		env = []string{"GH_HOST=" + host}
	}
	const fields = "number,title,state,isDraft,url,body,author," +
		"headRefName,baseRefName,comments,statusCheckRollup"
	var v gitHubPR
	err := r.runJSON(ctx, env, &v, "gh", "pr", "view", "--json", fields)
	if isNoPR(err, "no pull requests found") {
		return nil, ErrNoPR
	}
	if err != nil {
		return nil, err
	}
	pr := &PR{
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
	// A check run has a conclusion. A commit status has a state.
	var checks []Check
	for _, c := range v.StatusCheckRollup {
		checks = append(checks, Check{
			Name:   cmp.Or(c.Name, c.Context),
			Result: gitHubResult(cmp.Or(c.Conclusion, c.State)),
		})
	}
	pr.Checks, pr.Tally = keepChecks(checks, maxChecks)
	for _, c := range lastN(v.Comments, maxComments) {
		pr.Comments = append(pr.Comments, PRComment{
			Author: c.Author.Login,
			When:   c.CreatedAt,
			Body:   c.Body,
		})
	}
	return pr, nil
}

// keepChecks keeps failed checks first, then the earliest others, in order.
func keepChecks(checks []Check, n int) ([]Check, Tally) {
	var t Tally
	for _, c := range checks {
		t.add(c.Result)
	}
	if len(checks) <= n {
		return checks, t
	}
	others := max(n-t.Fail, 0)
	kept := make([]Check, 0, n)
	for _, c := range checks {
		if len(kept) == n {
			break
		}
		if c.Result == ResultFail {
			kept = append(kept, c)
		} else if others > 0 {
			kept = append(kept, c)
			others--
		}
	}
	return kept, t
}

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

// gitLabState treats a locked merge request as closed.
func gitLabState(s string) PRState {
	switch s {
	case "opened":
		return PROpen
	case "merged":
		return PRMerged
	}
	return PRClosed
}

// azureState treats an abandoned pull request as closed.
func azureState(s string) PRState {
	switch s {
	case "active":
		return PROpen
	case "completed":
		return PRMerged
	}
	return PRClosed
}

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

type gitLabUser struct {
	Username string `json:"username"`
}

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

type gitLabDiscussion struct {
	Notes []struct {
		Body      string     `json:"body"`
		CreatedAt string     `json:"created_at"`
		System    bool       `json:"system"`
		Author    gitLabUser `json:"author"`
	} `json:"notes"`
}

func (r *Repo) gitLab(ctx context.Context) (*PR, error) {
	var v gitLabMR
	err := r.runJSON(ctx, nil, &v, "glab", "mr", "view", "--output", "json")
	if isNoPR(err, "no open merge request") {
		return nil, ErrNoPR
	}
	if err != nil {
		return nil, err
	}
	pr := &PR{
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
	// The comments are optional. If glab cannot list them, leave them out.
	var ds []gitLabDiscussion
	args := []string{"mr", "note", "list", "--output", "json"}
	if err := r.runJSON(ctx, nil, &ds, "glab", args...); err == nil {
		pr.Comments = gitLabComments(ds)
	}
	return pr, nil
}

// gitLabComments skips system notes, such as "added 1 commit".
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

func (r *Repo) azure(ctx context.Context) (*PR, error) {
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
		return nil, ErrNoPR
	}
	p := v[0]
	return &PR{
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
