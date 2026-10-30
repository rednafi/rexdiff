// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"cmp"
	"image/color"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Cache is a render cache. It is not safe for concurrent use.
type Cache struct {
	lines map[syntaxKey]string
	size  int // bytes

	lexer struct {
		path, content string
		l             chroma.Lexer
	}

	diff struct {
		oldText, newText string
		hunks            []*hunk
		keep             [][]bool
		gen              int
	}
	folded foldedDiff
}

const maxCacheBytes = 32 << 20

func (c *Cache) put(key syntaxKey, s string) {
	// key.source is a substring of the file. The copy lets the file be freed.
	key.source = strings.Clone(key.source)
	n := len(key.source) + len(s)
	if c.lines == nil || c.size+n > maxCacheBytes {
		c.lines, c.size = make(map[syntaxKey]string), 0
	}
	c.lines[key] = s
	c.size += n
}

// syntaxKey has noColor because lipgloss.NoColor has the same RGBA value
// as black.
type syntaxKey struct {
	lexer, source string
	noColor       bool
	r, g, b, a    uint32
}

func newSyntaxKey(lexer, source string, bg color.Color) syntaxKey {
	key := syntaxKey{lexer: lexer, source: source}
	if _, none := bg.(lipgloss.NoColor); bg == nil || none {
		key.noColor = true
		return key
	}
	key.r, key.g, key.b, key.a = bg.RGBA()
	return key
}

// Chroma lexer time grows with the square of the line length, or worse.
const (
	maxHighlightLine = 1500
	maxHighlightFile = 512 << 10
)

// Some lexers are very slow on unusual text. Lines are highlighted during
// a draw, so long lines get a time limit.
const (
	lineBudget     = 5 * time.Millisecond
	budgetFreeLine = 256
)

func highlightLine(lexer chroma.Lexer, source string, ts *tokenStyler, value func(string) string) (out string, ok bool) {
	it, err := lexer.Tokenise(nil, source)
	if err != nil {
		return "", false
	}
	var deadline time.Time
	if len(source) > budgetFreeLine {
		deadline = time.Now().Add(lineBudget)
	}
	return renderTokens(it, ts, value, deadline)
}

// slowLexers are never used. On some short lines, Jungle and JSONata never
// stop. The others can take far longer than lineBudget for one token.
var slowLexers = []string{"Devicetree", "JSONata", "Jungle", "LilyPond", "Markless", "SYSTEMD"}

func (v *View) highlightCode(source string, bg color.Color) string {
	// Chroma treats a lone CR as a line break.
	source = strings.ReplaceAll(source, "\r", CRSymbol)
	if len(source) > maxHighlightLine {
		return Escape(source)
	}
	lexer := v.chromaLexer()
	key := newSyntaxKey(lexer.Config().Name, source, bg)
	if cached, ok := v.cache.lines[key]; ok {
		return cached
	}
	out, ok := highlightLine(lexer, source, newTokenStyler(bg), escapeChromaValue)
	if !ok {
		// Cache the plain line too. Otherwise a slow line can get colours
		// on a later draw and flicker.
		out = Escape(source)
	}
	v.cache.put(key, out)
	return out
}

// it must not be coalesced. A coalesced lexer can take very long to
// return a token, or never return one.
func renderTokens(it chroma.Iterator, ts *tokenStyler, value func(string) string, deadline time.Time) (out string, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	var b, run strings.Builder
	var t chroma.TokenType
	for token := it(); token != chroma.EOF; token = it() {
		if !deadline.IsZero() && time.Now().After(deadline) {
			return "", false
		}
		if token.Value == "" {
			continue
		}
		if token.Type != t && run.Len() > 0 {
			b.WriteString(ts.render(t, value(run.String())))
			run.Reset()
		}
		t = token.Type
		run.WriteString(token.Value)
	}
	if run.Len() > 0 {
		b.WriteString(ts.render(t, value(run.String())))
	}
	return b.String(), true
}

func escapeChromaValue(value string) string {
	return Escape(strings.TrimRight(value, "\n"))
}

func (v *View) chromaLexer() chroma.Lexer {
	lx := &v.cache.lexer
	content := cmp.Or(v.oldText, v.newText)
	if lx.l == nil || lx.path != v.path || lx.content != content {
		lx.path, lx.content = v.path, content
		lx.l = fileLexer(lx.path, lx.content)
	}
	return lx.l
}

const maxAnalyseBytes = 64 << 10

// fileLexer returns a lexer that is not coalesced.
func fileLexer(path, content string) chroma.Lexer {
	lexer := matchLexer(path)
	if lexer == nil {
		lexer = lexers.Analyse(content[:min(len(content), maxAnalyseBytes)])
	}
	if lexer == nil || slices.Contains(slowLexers, lexer.Config().Name) {
		lexer = lexers.Fallback
	}
	return lexer
}

// lexerCache holds nil for a file name with no match.
var (
	lexerCacheMu sync.RWMutex
	lexerCache   = map[string]chroma.Lexer{}
)

func matchLexer(fileName string) chroma.Lexer {
	lexerCacheMu.RLock()
	lexer, ok := lexerCache[fileName]
	lexerCacheMu.RUnlock()
	if ok {
		return lexer
	}
	lexer = lexers.Match(fileName)
	lexerCacheMu.Lock()
	lexerCache[fileName] = lexer
	lexerCacheMu.Unlock()
	return lexer
}

// tokenStyler uses the ANSI palette. It is not safe for concurrent use.
type tokenStyler struct {
	plain lipgloss.Style
	parts map[chroma.TokenType][2]string // escape codes before and after the text
}

func newTokenStyler(bg color.Color) *tokenStyler {
	return &tokenStyler{
		plain: lipgloss.NewStyle().Background(bg),
		parts: map[chroma.TokenType][2]string{},
	}
}

func (ts *tokenStyler) style(t chroma.TokenType) lipgloss.Style {
	if c, italic, ok := ansiFor(t); ok {
		return ts.plain.Foreground(lipgloss.Color(c)).Italic(italic)
	}
	return ts.plain
}

// render returns renderLines(ts.style(t), value), but faster.
func (ts *tokenStyler) render(t chroma.TokenType, value string) string {
	if strings.ContainsAny(value, "\t\r") {
		return renderLines(ts.style(t), value) // the style changes tabs and CRs
	}
	p, ok := ts.parts[t]
	if !ok {
		// Escape codes have only digits and punctuation, so not X.
		before, after, _ := strings.Cut(ts.style(t).Render("X"), "X")
		p = [2]string{before, after}
		ts.parts[t] = p
	}
	if p == [2]string{} {
		return value
	}
	// Style each line on its own, so a line shown alone keeps its colour.
	return p[0] + strings.ReplaceAll(value, "\n", p[1]+"\n"+p[0]) + p[1]
}

// Highlighter lexes a whole source in the background, so tokens that span
// lines get the correct colour. While [Highlighter.Busy] is true, draw
// again. The methods are safe for concurrent use.
type Highlighter struct {
	mu      sync.Mutex
	grew    chan struct{} // closed, and replaced, when lexing adds lines or ends
	lines   []string
	done    bool // lines holds every line
	stop    atomic.Bool
	running bool
	// Line does not wait again until lexing passes line lateUntil-1.
	lateUntil int

	plain    []string
	fallback func(string) string

	// Used only by the background goroutine.
	lexer   chroma.Lexer    // nil to lex the whole source
	it      chroma.Iterator // not coalesced
	ts      *tokenStyler
	process func(string) string
	cur     strings.Builder
	run     strings.Builder
	runType chroma.TokenType
	src     string // for fallback after a panic
	spent   time.Duration
}

const lexBatch = 256

const lexBudget = 2 * time.Second

// Line runs during a draw and holds up the next key press.
const lineWait = 25 * time.Millisecond

// NewHighlighter passes the text of each token through processValue. If
// chroma fails or panics, the lines from that point come from fallback.
func NewHighlighter(path, src string, bg color.Color, processValue, fallback func(string) string) *Highlighter {
	h := &Highlighter{grew: make(chan struct{}), plain: strings.Split(src, "\n"), fallback: fallback}
	h.ts, h.process = newTokenStyler(bg), processValue
	lexer := fileLexer(path, src)
	if len(src) > maxHighlightFile || slices.ContainsFunc(h.plain, func(l string) bool { return len(l) > maxHighlightLine }) {
		h.lexer = lexer
	} else {
		// Not coalesced, so Stop and lexBudget act between any two tokens.
		it, err := lexer.Tokenise(nil, src)
		if err != nil {
			h.lines, h.done = strings.Split(fallback(src), "\n"), true
			return h
		}
		h.it, h.src = it, src
	}
	h.start()
	return h
}

// start needs h.mu held, or h not yet shared.
func (h *Highlighter) start() {
	h.running = true
	h.stop.Store(false)
	go h.lexAll(len(h.lines))
}

func (h *Highlighter) lexAll(n int) {
	for {
		lines, eof := h.lex(n, lexBatch)
		n += len(lines)
		h.mu.Lock()
		h.lines = append(h.lines, lines...)
		h.done = eof
		stop := eof || h.stop.Load()
		if stop {
			h.running = false
		}
		close(h.grew)
		h.grew = make(chan struct{})
		h.mu.Unlock()
		if stop {
			return
		}
	}
}

func (h *Highlighter) lex(done, n int) (lines []string, eof bool) {
	start := time.Now()
	defer func() { h.spent += time.Since(start) }()
	overBudget := func() bool { return h.spent+time.Since(start) > lexBudget }
	if h.lexer != nil {
		return h.lexLines(done, n, overBudget)
	}
	return h.lexTokens(done, n, overBudget)
}

func (h *Highlighter) lexLines(done, n int, overBudget func() bool) (lines []string, eof bool) {
	end := min(done+n, len(h.plain))
	for _, l := range h.plain[done:end] {
		switch {
		case h.stop.Load():
			return lines, false
		case overBudget():
			lines = append(lines, h.fallback(l))
		default:
			lines = append(lines, h.lexLine(l))
		}
	}
	return lines, end == len(h.plain)
}

func (h *Highlighter) lexTokens(done, n int, overBudget func() bool) (lines []string, eof bool) {
	for len(lines) < n {
		if h.stop.Load() {
			return lines, false
		}
		token, ok := h.next()
		if !ok || overBudget() {
			return append(lines, h.plainFrom(done+len(lines))...), true
		}
		if token.Value == "" && token != chroma.EOF {
			continue
		}
		if h.run.Len() > 0 && (token.Type != h.runType || token == chroma.EOF) {
			lines = h.addRun(lines)
		}
		if token == chroma.EOF {
			lines = append(lines, h.cur.String())
			h.it, h.cur, h.src = nil, strings.Builder{}, ""
			return lines, true
		}
		h.runType = token.Type
		h.run.WriteString(token.Value)
	}
	return lines, false
}

// addRun keeps the start of the next line in h.cur.
func (h *Highlighter) addRun(lines []string) []string {
	s := h.ts.render(h.runType, h.process(h.run.String()))
	h.run.Reset()
	for {
		before, after, found := strings.Cut(s, "\n")
		h.cur.WriteString(before)
		if !found {
			return lines
		}
		lines = append(lines, h.cur.String())
		h.cur.Reset()
		s = after
	}
}

func (h *Highlighter) plainFrom(i int) []string {
	all := strings.Split(h.fallback(h.src), "\n")
	h.it, h.cur, h.run, h.src = nil, strings.Builder{}, strings.Builder{}, ""
	return all[min(i, len(all)):]
}

func (h *Highlighter) lexLine(l string) string {
	if len(l) <= maxHighlightLine {
		value := func(v string) string { return h.process(strings.TrimRight(v, "\n")) }
		if out, ok := highlightLine(h.lexer, l, h.ts, value); ok {
			return out
		}
	}
	return h.fallback(l)
}

func (h *Highlighter) next() (token chroma.Token, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return h.it(), true
}

// Line returns the 0-based line i, or ok false past the last line. If the
// line is not ready within lineWait, it comes back plain with final false.
// After Stop, Line starts the goroutine again.
func (h *Highlighter) Line(i int) (line string, ok, final bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.lines) <= i && !h.done && !h.running {
		h.start()
	}
	var timeout <-chan time.Time
	for len(h.lines) <= i && !h.done && len(h.lines) >= h.lateUntil {
		if timeout == nil {
			t := time.NewTimer(lineWait)
			defer t.Stop()
			timeout = t.C
		}
		grew := h.grew
		h.mu.Unlock()
		select {
		case <-grew:
			h.mu.Lock()
		case <-timeout:
			h.mu.Lock()
			h.lateUntil = i + 1
		}
	}
	switch {
	case i < len(h.lines):
		return h.lines[i], true, true
	case h.done:
		return "", false, true
	}
	h.lateUntil = max(h.lateUntil, i+1)
	if i < len(h.plain) {
		return h.fallback(h.plain[i]), true, false
	}
	return "", false, false
}

// Busy reports whether the background goroutine is still highlighting.
func (h *Highlighter) Busy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running && !h.done
}

// Stop ends the background goroutine after its current token or line.
func (h *Highlighter) Stop() { h.stop.Store(true) }

func ansiFor(t chroma.TokenType) (c string, italic, ok bool) {
	switch {
	case t.InCategory(chroma.Comment):
		return ansiGrey, true, true
	case t == chroma.KeywordType, t == chroma.NameBuiltin,
		t.InSubCategory(chroma.NameClass):
		return ansiCyan, false, true
	case t.InCategory(chroma.Keyword):
		return ansiMagenta, false, true
	case t.InSubCategory(chroma.LiteralString):
		return ansiGreen, false, true
	case t.InSubCategory(chroma.LiteralNumber), t == chroma.NameConstant:
		return ansiYellow, false, true
	case t == chroma.NameFunction, t == chroma.NameFunctionMagic,
		t == chroma.NameTag, t == chroma.NameAttribute:
		return ansiBlue, false, true
	}
	return "", false, false
}

// renderLines styles each line separately. This avoids the padding that
// Lip Gloss adds to a multi-line string, such as a CommentSingle token.
func renderLines(s lipgloss.Style, value string) string {
	return EachLine(value, func(l string) string { return s.Render(l) })
}
