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
	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/vcs"
)

// The PR tab shows the pull request for the current branch. It is
// read-only.

// Timing for fetching the pull request.
const (
	// prInterval is the time between fetches while the PR tab is shown.
	// Polling is needed because changes on the forge leave no trace in
	// the local repository.
	prInterval = time.Minute
	// prTimeout limits how long one fetch can take.
	prTimeout = time.Minute
)

// prFetch tracks fetches of the pull request.
type prFetch struct {
	gen      int       // identifies the latest fetch; results of older fetches are stale
	inFlight bool      // fetch gen is in progress
	polled   time.Time // the time of the last request, for prInterval
	cancel   context.CancelFunc
	// trailing is true if a non-forced request arrived during a fetch,
	// so another fetch follows when it finishes.
	trailing bool
	branch   string // the branch at the last ref change
	seen     bool   // the watcher has reported refs at least once
}

// requestPR asks for the pull request to be fetched. A forced request
// starts a fetch at once. A non-forced request made during a fetch starts
// another fetch after it finishes. A fetch that hangs is cancelled after
// prTimeout.
func (m *model) requestPR(force bool) tea.Cmd {
	f := &m.pr.fetch
	f.polled = time.Now()
	if !force && f.inFlight {
		f.trailing = true
		return nil
	}
	return m.fetchPR()
}

// fetchPR cancels any fetch in progress and returns a command that
// fetches the pull request for the branch.
func (m *model) fetchPR() tea.Cmd {
	f := &m.pr.fetch
	if f.cancel != nil {
		f.cancel()
	}
	ctx, cancel := xctx.WithTimeout(m.ctx, prTimeout, "pull request read")
	f.gen++
	f.inFlight, f.trailing, f.cancel = true, false, cancel
	repo, gen := m.repo, f.gen
	hosts := vcs.Hosts{
		GitHub: m.cfg.GitHubHost,
		GitLab: m.cfg.GitLabHost,
		Azure:  m.cfg.AzureHost,
	}
	return func() tea.Msg {
		defer cancel()
		pr, err := repo.PR(ctx, hosts)
		return prMsg{gen: gen, pr: pr, err: err}
	}
}

// reloadPR fetches the pull request again after the refs change. After a
// branch switch, which a detached HEAD during a rebase is not, the PR tab
// is blank until the fetch finishes.
func (m *model) reloadPR() tea.Cmd {
	f := &m.pr.fetch
	ctx, done := m.gitContext("branch")
	b, err := m.repo.Branch(ctx)
	done()
	if err != nil || b == "HEAD" {
		b = f.branch
	}
	oldBranch := f.branch
	f.branch = b
	if !f.seen { // the first report comes at startup, and Init already fetches the PR
		f.seen = true
		return nil
	}
	if oldBranch != "" && b != oldBranch {
		m.pr.pr, m.pr.err = nil, ""
		cmd := m.requestPR(true)
		m.renderPR()
		return cmd
	}
	return m.requestPR(false)
}

// prStateStyle returns the style used to show the pull request state s.
func prStateStyle(s vcs.PRState) lipgloss.Style {
	switch s {
	case vcs.PROpen:
		return addedStyle
	case vcs.PRMerged:
		return headerStyle
	case vcs.PRClosed:
		return deletedStyle
	}
	return dimStyle
}

// renderPR renders the pull request, or a note explaining its absence,
// into the PR tab.
func (m *model) renderPR() {
	p := &m.pr.pane
	p.path, p.note = "", ""
	pr := m.pr.pr
	switch {
	case m.pr.err != "":
		p.note = m.pr.err
		p.set(nil, nil)
		return
	case pr == nil:
		if !m.pr.fetch.inFlight {
			p.note = vcs.ErrNoPR.Error()
		}
		p.set(nil, nil)
		return
	}
	r := m.markdownRenderer(m.codeWidth() - 4)
	lines := slices.Concat([]string{""}, prHeader(pr))
	lines = append(lines, checkLines(pr)...)
	lines = append(lines, "")
	// A poll usually finds the same texts, so the texts rendered last time
	// come from a cache.
	mc := &m.markdown
	cached := mc.pr
	if r == nil || mc.r != r {
		cached = nil
	}
	used := map[string][]string{}
	truncated := dimStyle.Render("  … truncated · " + firstKey(m.keys.OpenPR) + " to open in browser")
	work := 0 // the work of the texts rendered so far, as markdownWork counts it
	render := func(text string) []string {
		src, cut := m.prText(text)
		note, w := prTextNote(src)
		if note == "" && work+w > maxMarkdownWork {
			note = notePRTextTooBig
		}
		out, ok := cached[src]
		switch {
		case note != "":
			out = sourceLines(note, src, m.codeWidth()-4)
		case !ok:
			out = renderMarkdown(r, src)
		}
		if note == "" {
			work += w
			used[src] = out
		}
		if cut {
			out = append(slices.Clip(out), "", truncated)
		}
		return out
	}
	if strings.TrimSpace(pr.Body) != "" {
		lines = append(lines, render(pr.Body)...)
	}
	now := time.Now()
	for _, c := range pr.Comments {
		author := headerStyle.Render(c.Author) + "  " + dimStyle.Render(age(c.When, now))
		lines = append(lines, "", "  "+author)
		lines = append(lines, render(c.Body)...)
	}
	if r != nil && mc.r == r {
		mc.pr = used
	}
	p.set(lines, make([]diff.Row, len(lines)))
}

// Limits on the PR text that rexdiff renders, because a long or hostile
// body would make each poll slow. A body or comment longer than
// maxPRTextBytes is cut, with a note to open the PR in the browser. Only
// the first maxDetails <details> tags in each text become summary lines.
// A text is also subject to the limits on a block of Markdown in a file,
// and the texts of a PR to the limit on the work of a file: a text over a
// limit shows as its source.
const (
	maxPRTextBytes = 16 << 10
	maxDetails     = 64
)

// Notes for PR text that rexdiff does not render.
const (
	notePRTextTooBig  = "  text too large to render · source below"
	notePRTextTooDeep = "  text nested too deeply to render · source below"
)

// prTextNote returns the note to show instead of the rendered PR text
// src, or "" if it can be rendered. work is the work of rendering it, as
// markdownWork counts it, or 0 if it is not rendered.
func prTextNote(src string) (note string, work int) {
	switch note, work := blockNote(src); note {
	case "":
		return "", work
	case noteBlockTooDeep:
		return notePRTextTooDeep, 0
	}
	return notePRTextTooBig, 0
}

// prText returns the Markdown to render for src, a PR body or comment,
// with its <details> blocks replaced by renderDetails. A src longer than
// maxPRTextBytes is cut at the last line break before the limit, and cut
// is true.
func (m *model) prText(src string) (md string, cut bool) {
	if len(src) > maxPRTextBytes {
		src, cut = strings.ToValidUTF8(src[:maxPRTextBytes], ""), true
		if i := strings.LastIndexByte(src, '\n'); i > 0 {
			src = src[:i]
		}
	}
	return m.renderDetails(src), cut
}

// detailsTagRE matches an opening or closing <details> tag. summaryRE
// matches the summary at the start of a block, capturing its text.
var (
	detailsTagRE = regexp.MustCompile(`(?i)<details[^>]*>|</details>`)
	summaryRE    = regexp.MustCompile(`(?is)\A\s*<summary[^>]*>(.*?)</summary>`)
)

// renderDetails returns src with each <details> block replaced by its
// summary line. The line starts with ▸ for a closed block and ▾ for an
// open block. The body of an open block follows the line. A block must
// start with a <summary>. A nested block is replaced before the block
// around it, and a closing tag ends the innermost open block that has a
// summary. Tags that do not form a block, and the tags after the first
// maxDetails opening tags, stay as they are.
func (m *model) renderDetails(src string) string {
	// open holds the opening tag and the text so far of each block that
	// has not been closed, innermost last. Text outside any block goes in
	// out.
	type block struct {
		tag  string
		text strings.Builder
	}
	var open []*block
	var out strings.Builder
	emit := func(s string) {
		if len(open) == 0 {
			out.WriteString(s)
		} else {
			open[len(open)-1].text.WriteString(s)
		}
	}
	at, opened := 0, 0
	for _, t := range detailsTagRE.FindAllStringIndex(src, -1) {
		tag := src[t[0]:t[1]]
		closing := strings.HasPrefix(tag, "</")
		if !closing && opened == maxDetails {
			continue
		}
		emit(src[at:t[0]])
		at = t[1]
		if !closing {
			opened++
			open = append(open, &block{tag: tag})
			continue
		}
		done := false
		for !done && len(open) > 0 {
			b := open[len(open)-1]
			open = open[:len(open)-1]
			text := b.text.String()
			if sub := summaryRE.FindStringSubmatchIndex(text); sub != nil {
				emit(m.detailsLine(text[sub[2]:sub[3]], text[sub[1]:]))
				done = true
			} else {
				emit(b.tag + text) // not a block, so its tag is text
			}
		}
		if !done {
			emit(tag)
		}
	}
	emit(src[at:])
	for _, b := range open { // blocks that are never closed are text
		out.WriteString(b.tag)
		out.WriteString(b.text.String())
	}
	return out.String()
}

// detailsLine returns the Markdown that replaces a <details> block with
// the given summary and body.
func (m *model) detailsLine(summary, body string) string {
	summary = strings.Join(strings.Fields(summary), " ")
	if m.pr.detailsExpanded {
		return "\n\n▾ " + summary + "\n\n" + body + "\n\n"
	}
	return "\n\n▸ " + summary + "\n\n"
}

// age returns the time elapsed from when to now in short form, such as
// "5m" or "3d". when is an RFC 3339 time. It returns "" if when cannot be
// parsed, and "0s" if when is after now.
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

// prHeader returns the title line and the status line of pr.
func prHeader(pr *vcs.PR) []string {
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

// checkLines returns a line with the counts of all the checks, followed
// by the names of the failed checks that the pull request lists and a
// note of how many failed checks it leaves out. A skipped check counts as
// passed. It returns nil if there are no checks.
func checkLines(pr *vcs.PR) []string {
	t := pr.Tally
	if t.Total() == 0 {
		return nil
	}
	lines := []string{fmt.Sprintf(
		"  checks: %s  %s  %s",
		addedStyle.Render(fmt.Sprintf("✓ %d", t.Pass+t.Skip)),
		deletedStyle.Render(fmt.Sprintf("✗ %d", t.Fail)),
		noteStyle.Render(fmt.Sprintf("◌ %d", t.Pending)),
	)}
	listed := 0
	for _, c := range pr.Checks {
		if c.Result == vcs.ResultFail {
			lines = append(lines, "    "+deletedStyle.Render("✗ ")+c.Name)
			listed++
		}
	}
	if n := t.Fail - listed; n > 0 {
		lines = append(lines, dimStyle.Render("    … "+plural(n, "more failed check")))
	}
	return lines
}

// onPR shows the pull request that fetchPR fetched, unless a newer fetch
// has started since. If a request arrived during the fetch, it starts
// another fetch.
func (m *model) onPR(msg prMsg) tea.Cmd {
	f := &m.pr.fetch
	if msg.gen != f.gen {
		return nil
	}
	f.inFlight, f.cancel = false, nil
	var next tea.Cmd
	if f.trailing {
		next = m.fetchPR()
	}
	// If a fetch fails, keep showing the last pull request that was
	// fetched. ErrNoPR does not count as a failure.
	if msg.err != nil && m.pr.pr != nil && !errors.Is(msg.err, vcs.ErrNoPR) {
		m.flash("refresh failed: " + msg.err.Error())
		m.renderPR()
		return next
	}
	m.pr.pr, m.pr.err = msg.pr, ""
	if msg.err != nil {
		m.pr.err = msg.err.Error()
	}
	m.renderPR()
	// Restore the scroll position saved before a restart.
	if p := m.pr.place; p != nil && msg.err == nil && msg.pr != nil {
		m.pr.place = nil
		m.pr.pane.restore(*p)
	}
	return next
}
