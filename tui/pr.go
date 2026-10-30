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

const (
	// Poll because changes on the forge leave no trace in the local
	// repository.
	prInterval = time.Minute
	prTimeout  = time.Minute
)

type prFetch struct {
	gen      int // the latest fetch. Results of older fetches are stale.
	inFlight bool
	polled   time.Time
	cancel   context.CancelFunc
	// trailing is true if a non-forced request arrived during a fetch.
	trailing bool
	branch   string // the branch at the last ref change
	seen     bool   // the watcher has reported refs at least once
}

func (m *model) requestPR(force bool) tea.Cmd {
	f := &m.pr.fetch
	f.polled = time.Now()
	if !force && f.inFlight {
		f.trailing = true
		return nil
	}
	return m.fetchPR()
}

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

// A detached HEAD during a rebase is not a branch switch.
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

// Limits on PR text, because a long or hostile body makes each poll slow.
// The Markdown block and file work limits also apply.
const (
	maxPRTextBytes = 16 << 10
	maxDetails     = 64
)

const (
	notePRTextTooBig  = "  text too large to render · source below"
	notePRTextTooDeep = "  text nested too deeply to render · source below"
)

func prTextNote(src string) (note string, work int) {
	switch note, work := blockNote(src); note {
	case "":
		return "", work
	case noteBlockTooDeep:
		return notePRTextTooDeep, 0
	}
	return notePRTextTooBig, 0
}

func (m *model) prText(src string) (md string, cut bool) {
	if len(src) > maxPRTextBytes {
		src, cut = strings.ToValidUTF8(src[:maxPRTextBytes], ""), true
		if i := strings.LastIndexByte(src, '\n'); i > 0 {
			src = src[:i]
		}
	}
	return m.renderDetails(src), cut
}

var (
	detailsTagRE = regexp.MustCompile(`(?i)<details[^>]*>|</details>`)
	summaryRE    = regexp.MustCompile(`(?is)\A\s*<summary[^>]*>(.*?)</summary>`)
)

// renderDetails replaces each <details> block with its summary line. A
// block must start with a <summary>. Tags that do not form a block stay as
// they are. So do the tags after the first maxDetails opening tags.
func (m *model) renderDetails(src string) string {
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

func (m *model) detailsLine(summary, body string) string {
	summary = strings.Join(strings.Fields(summary), " ")
	if m.pr.detailsExpanded {
		return "\n\n▾ " + summary + "\n\n" + body + "\n\n"
	}
	return "\n\n▸ " + summary + "\n\n"
}

// age parses when as RFC 3339.
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
	// Restore the scroll position from before a restart.
	if p := m.pr.place; p != nil && msg.err == nil && msg.pr != nil {
		m.pr.place = nil
		m.pr.pane.restore(*p)
	}
	return next
}
