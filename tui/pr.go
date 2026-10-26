package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/vcs"
)

// The PR tab shows the pull request of the branch. The tab cannot change
// the pull request.

// The times of the reads of the pull request, as in herdr-reviewr.
const (
	// prPoll is the time between two reads while the PR tab shows. A
	// change on the forge has no local sign.
	prPoll = time.Minute
	// prTimeout is the longest time of a read.
	prTimeout = time.Minute
)

// prFetch follows the reads of the pull request.
type prFetch struct {
	gen      int       // the read whose result shows. Other reads are stale.
	inFlight bool      // the read gen runs
	polled   time.Time // the last request, for prPoll
	cancel   context.CancelCauseFunc
	// trailing is true if an ambient request comes while a read runs.
	// Then one more read follows that read.
	trailing bool
	branch   string // the branch at the last change of the refs
	seen     bool   // the watcher reports the refs once
}

// requestPR asks for a read of the pull request. A forced request starts
// a read at once. An ambient request during a read starts one more read
// after it. A read that hangs ends after prTimeout.
func (m *model) requestPR(forced bool) tea.Cmd {
	f := &m.prf
	f.polled = time.Now()
	if !forced && f.inFlight {
		f.trailing = true
		return nil
	}
	return m.loadPR()
}

// loadPR stops the read that runs, and returns a command that reads the
// pull request of the branch.
func (m *model) loadPR() tea.Cmd {
	f := &m.prf
	if f.cancel != nil {
		f.cancel(fmt.Errorf("pull request read: a newer read replaces it: %w", context.Canceled))
	}
	ctx, cancel := context.WithCancelCause(m.ctx)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, prTimeout,
		fmt.Errorf("pull request read: %v timeout: %w", prTimeout, context.DeadlineExceeded))
	f.gen++
	f.inFlight, f.trailing, f.cancel = true, false, cancel
	repo, gen := m.repo, f.gen
	hosts := vcs.Hosts{
		GitHub: m.cfg.GitHubHost,
		GitLab: m.cfg.GitLabHost,
		Azure:  m.cfg.AzureHost,
	}
	return func() tea.Msg {
		defer cancelTimeout()
		defer cancel(fmt.Errorf("pull request read: done: %w", context.Canceled))
		pr, err := repo.PullRequest(ctx, hosts)
		return prMsg{gen: gen, pr: pr, err: err}
	}
}

// refsChanged reads the pull request again after a change of the refs.
// After a switch to another branch, the PR tab is blank until the read
// ends. A detached HEAD keeps the branch, so a rebase is not a switch.
func (m *model) refsChanged() tea.Cmd {
	f := &m.prf
	b, err := m.repo.Branch(m.ctx)
	if err != nil || b == "HEAD" {
		b = f.branch
	}
	old := f.branch
	f.branch = b
	if !f.seen { // the first report is the start, and Init reads the PR
		f.seen = true
		return nil
	}
	if old != "" && b != old {
		m.pr = prState{}
		cmd := m.requestPR(true)
		m.renderPR()
		return cmd
	}
	return m.requestPR(false)
}

// prStateStyle returns the style that shows the state of a pull request.
func prStateStyle(s vcs.PRState) lipgloss.Style {
	switch s {
	case vcs.PROpen:
		return addStyle
	case vcs.PRMerged:
		return headerStyle
	case vcs.PRClosed:
		return delStyle
	}
	return dimStyle
}

// checkSummary counts the checks that pass, fail, or are pending, and
// returns the names of the checks that fail. A skipped check counts as a
// pass.
func checkSummary(checks []vcs.Check) (pass, fail, pend int, failed []string) {
	for _, c := range checks {
		switch c.Result {
		case vcs.ResultPass, vcs.ResultSkip:
			pass++
		case vcs.ResultFail:
			fail++
			failed = append(failed, c.Name)
		default:
			pend++
		}
	}
	return pass, fail, pend, failed
}

// renderPR shows the pull request in the PR tab.
func (m *model) renderPR() {
	p := &m.prPane
	p.path, p.note = "", ""
	pr := m.pr.pr
	switch {
	case m.pr.err != "":
		p.note = m.pr.err
		p.set(nil, nil)
		return
	case pr == nil:
		if !m.prf.inFlight {
			p.note = vcs.ErrNoPullRequest.Error()
		}
		p.set(nil, nil)
		return
	}
	md := m.markdown(m.codeWidth() - 4)
	lines := append([]string{""}, prHeader(pr)...)
	lines = append(lines, checkLines(pr.Checks)...)
	lines = append(lines, "")
	if strings.TrimSpace(pr.Body) != "" {
		lines = append(lines, renderMarkdown(md, m.details(pr.Body))...)
	}
	now := time.Now()
	for _, c := range pr.Comments {
		author := headerStyle.Render(c.Author) + "  " + dimStyle.Render(age(c.When, now))
		lines = append(lines, "", "  "+author)
		lines = append(lines, renderMarkdown(md, m.details(c.Body))...)
	}
	p.set(lines, make([]diff.Row, len(lines)))
}

// detailsRE matches a <details> block with its summary and its body.
// detailsOpenRE matches the start of a block.
var (
	detailsRE = regexp.MustCompile(
		`(?is)\A<details[^>]*>\s*<summary[^>]*>(.*?)</summary>(.*?)</details>`)
	detailsOpenRE = regexp.MustCompile(`(?i)<details[^>]*>`)
)

// details returns src with each <details> block as its summary line, as
// herdr-reviewr shows it. The line starts with ▸ for a closed block, and
// with ▾ for an open block. The body of an open block follows the line.
// The last block is done first, so an inner block is done before the
// block that holds it.
func (m *model) details(src string) string {
	for _, o := range slices.Backward(detailsOpenRE.FindAllStringIndex(src, -1)) {
		b := src[o[0]:]
		sub := detailsRE.FindStringSubmatchIndex(b)
		if sub == nil {
			continue
		}
		summary := strings.Join(strings.Fields(b[sub[2]:sub[3]]), " ")
		block := "\n\n▸ " + summary + "\n\n"
		if m.prDetails {
			block = "\n\n▾ " + summary + "\n\n" + b[sub[4]:sub[5]] + "\n\n"
		}
		src = src[:o[0]] + block + b[sub[1]:]
	}
	return src
}

// age returns the time from when to now, as herdr-reviewr shows it, for
// example "5m" or "3d". when is an RFC 3339 time. A bad time gives "". A
// time after now gives "0s".
func age(when string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, when)
	if err != nil {
		return ""
	}
	const day = 24 * time.Hour
	d := max(now.Sub(t), 0)
	for _, u := range []struct {
		size time.Duration
		name string
	}{{365 * day, "y"}, {7 * day, "w"}, {day, "d"}, {time.Hour, "h"}, {time.Minute, "m"}} {
		if d >= u.size {
			return fmt.Sprintf("%d%s", int64(d/u.size), u.name)
		}
	}
	return fmt.Sprintf("%ds", int64(d/time.Second))
}

// prHeader returns the title line and the state line of pr.
func prHeader(pr *vcs.PullRequest) []string {
	title := boldStyle.Render(pr.Title)
	number := dimStyle.Render(fmt.Sprintf("#%d", pr.Number))
	state := prStateStyle(pr.State).Render("● " + string(pr.State))
	return []string{
		fmt.Sprintf("  %s %s", title, number),
		fmt.Sprintf(
			"  %s  %s → %s  %s  %s",
			state,
			pr.Head,
			pr.Base,
			dimStyle.Render("by "+pr.Author),
			dimStyle.Render(pr.URL),
		),
	}
}

// checkLines returns the counts of the checks, and the names of the
// checks that fail. With no checks, it returns no lines.
func checkLines(checks []vcs.Check) []string {
	if len(checks) == 0 {
		return nil
	}
	pass, fail, pend, failed := checkSummary(checks)
	lines := []string{fmt.Sprintf(
		"  checks: %s  %s  %s",
		addStyle.Render(fmt.Sprintf("✓ %d", pass)),
		delStyle.Render(fmt.Sprintf("✗ %d", fail)),
		noteStyle.Render(fmt.Sprintf("◌ %d", pend)),
	)}
	for _, f := range failed {
		lines = append(lines, "    "+delStyle.Render("✗ ")+f)
	}
	return lines
}

// onPR shows the pull request that loadPR reads, unless a later read
// replaces it. After a read with a trailing request, onPR starts the next
// read.
func (m *model) onPR(msg prMsg) tea.Cmd {
	f := &m.prf
	if msg.gen != f.gen {
		return nil
	}
	f.inFlight, f.cancel = false, nil
	var next tea.Cmd
	if f.trailing {
		next = m.loadPR()
	}
	// As in herdr-reviewr, a failed read keeps the last good pull request.
	// The end of the pull request is not a failure.
	if msg.err != nil && m.pr.pr != nil && !errors.Is(msg.err, vcs.ErrNoPullRequest) {
		m.flash("refresh failed: " + msg.err.Error())
		m.renderPR()
		return next
	}
	m.pr = prState{pr: msg.pr}
	if msg.err != nil {
		m.pr.err = msg.err.Error()
	}
	m.renderPR()
	// prPlace is from before a restart.
	if p := m.prPlace; p != nil && msg.err == nil && msg.pr != nil {
		m.prPlace = nil
		m.prPane.restore(*p)
	}
	return next
}
