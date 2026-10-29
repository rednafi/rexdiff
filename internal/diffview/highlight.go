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

// Cache stores highlighted lines, the last lexer and the last diff for the
// views that share it. The zero value is an empty Cache. A Cache is not
// safe for concurrent use.
type Cache struct {
	lines map[syntaxKey]string
	size  int // the total size in bytes of the lines and their sources

	// lexer is the most recently used lexer.
	lexer struct {
		path, content string
		l             chroma.Lexer
	}

	// diff is the most recent diff. gen counts the diffs computed, starting
	// at 1.
	diff struct {
		oldText, newText string
		hunks            []*hunk
		keep             [][]bool // the keptLines of each hunk
		gen              int
	}
	folded foldedDiff // the most recent diff after folding
}

// maxCacheBytes limits the total size of the lines and sources in a Cache.
// When a new line would exceed it, the Cache is emptied first.
const maxCacheBytes = 32 << 20

// put stores the highlighted line s under key.
func (c *Cache) put(key syntaxKey, s string) {
	// key.source is a substring of the whole file. Copying it lets the
	// cache keep the line without keeping the whole file in memory.
	key.source = strings.Clone(key.source)
	n := len(key.source) + len(s)
	if c.lines == nil || c.size+n > maxCacheBytes {
		c.lines, c.size = make(map[syntaxKey]string), 0
	}
	c.lines[key] = s
	c.size += n
}

// syntaxKey is the key of a line in a [Cache]: the lexer, the source and
// the background colour. noColor is true when there is no background,
// because lipgloss.NoColor has the same RGBA value as black.
type syntaxKey struct {
	lexer, source string
	noColor       bool
	r, g, b, a    uint32
}

// newSyntaxKey returns the key for source highlighted by the lexer named
// lexer on the background bg.
func newSyntaxKey(lexer, source string, bg color.Color) syntaxKey {
	key := syntaxKey{lexer: lexer, source: source}
	if _, none := bg.(lipgloss.NoColor); bg == nil || none {
		key.noColor = true
		return key
	}
	key.r, key.g, key.b, key.a = bg.RGBA()
	return key
}

// Limits on syntax highlighting, because chroma's lexers take time that
// grows with the square of the length of a line, or worse. A line longer
// than maxHighlightLine bytes is drawn plain, with control characters
// escaped. A [Highlighter] lexes a file longer than maxHighlightFile bytes, or with
// a line longer than maxHighlightLine, one line at a time.
const (
	maxHighlightLine = 1500
	maxHighlightFile = 512 << 10
)

// Some lexers take far longer than the length of a line suggests on
// unusual text, such as a line of one repeated character, and the Changes
// tab highlights its lines while the screen is drawn. So highlighting a
// line on its own that is longer than budgetFreeLine bytes stops after
// lineBudget, and the line is drawn plain. Highlighting stops only between
// two tokens, so a line can take longer than lineBudget. A shorter line is
// always highlighted in full.
const (
	lineBudget     = 5 * time.Millisecond
	budgetFreeLine = 256
)

// highlightLine highlights source, a single line, with lexer, passing the
// text of each token through value. ok is false if the lexer fails or
// panics, or if source is longer than budgetFreeLine bytes and takes
// longer than lineBudget.
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

// slowLexers are the chroma lexers that are never used. On some short
// lines, Jungle and JSONata never stop, and the others take far longer
// than lineBudget for a single token, which lineBudget cannot cut short. Their files are drawn plain.
var slowLexers = []string{"Devicetree", "JSONata", "Jungle", "LilyPond", "Markless", "SYSTEMD"}

// highlightCode returns source with syntax highlighting on the background
// bg, and stores the result in the cache. If source is longer than
// maxHighlightLine or chroma fails, highlightCode returns source escaped
// and without colours.
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
		// Keep the plain line too, so that a line that was too slow once
		// does not get its colours on a later draw and flicker.
		out = Escape(source)
	}
	v.cache.put(key, out)
	return out
}

// renderTokens renders the tokens of it with ts, passing the text of each
// token through value first. Like [chroma.Coalesce], it joins adjacent
// tokens of the same type. ok is false if the lexer panics or the tokens
// are not all read by deadline, unless deadline is the zero time. it must
// come from a lexer that is not coalesced, because a coalesced lexer can
// take a long time to return a token, or never return one.
func renderTokens(it chroma.Iterator, ts *tokenStyler, value func(string) string, deadline time.Time) (out string, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	var b, run strings.Builder // run holds the text of the tokens of type t
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

// escapeChromaValue escapes the text of a token for the terminal and
// removes its trailing line breaks.
func escapeChromaValue(value string) string {
	return Escape(strings.TrimRight(value, "\n"))
}

// chromaLexer returns the lexer for the old file, or for the new file if
// the old file is empty, and stores it in the cache.
func (v *View) chromaLexer() chroma.Lexer {
	lx := &v.cache.lexer
	content := cmp.Or(v.oldText, v.newText)
	if lx.l == nil || lx.path != v.path || lx.content != content {
		lx.path, lx.content = v.path, content
		lx.l = fileLexer(lx.path, lx.content)
	}
	return lx.l
}

// maxAnalyseBytes limits the content that fileLexer looks at, because the
// time that chroma takes to pick a lexer grows with the content.
const maxAnalyseBytes = 64 << 10

// fileLexer returns the chroma lexer for the file at path. If no lexer
// matches the file name, it picks one based on the start of content. If
// none matches the content either, or the lexer is one of slowLexers, it
// returns the fallback lexer. The lexer is not coalesced.
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

// lexerCache maps each file name to its lexer, or to nil if none matches.
// lexerCacheMu guards it.
var (
	lexerCacheMu sync.RWMutex
	lexerCache   = map[string]chroma.Lexer{}
)

// matchLexer returns the chroma lexer for fileName, or nil if no lexer
// matches. It stores the result in lexerCache.
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

// tokenStyler colours chroma tokens with the terminal's ANSI palette on a
// background, ignoring the chroma style. It renders each style once and
// reuses its escape codes around the text of each token. A tokenStyler is
// not safe for concurrent use.
type tokenStyler struct {
	plain lipgloss.Style
	parts map[chroma.TokenType][2]string // the escape codes before and after the text
}

// newTokenStyler returns a tokenStyler for the background bg.
func newTokenStyler(bg color.Color) *tokenStyler {
	return &tokenStyler{
		plain: lipgloss.NewStyle().Background(bg),
		parts: map[chroma.TokenType][2]string{},
	}
}

// style returns the style for a token of type t.
func (ts *tokenStyler) style(t chroma.TokenType) lipgloss.Style {
	if c, italic, ok := ansiFor(t); ok {
		return ts.plain.Foreground(lipgloss.Color(c)).Italic(italic)
	}
	return ts.plain
}

// render returns the same result as renderLines(ts.style(t), value), but
// faster.
func (ts *tokenStyler) render(t chroma.TokenType, value string) string {
	if strings.ContainsAny(value, "\t\r") {
		return renderLines(ts.style(t), value) // the style changes tabs and CRs
	}
	p, ok := ts.parts[t]
	if !ok {
		// Escape codes contain only digits and punctuation, so they cannot
		// contain X.
		before, after, _ := strings.Cut(ts.style(t).Render("X"), "X")
		p = [2]string{before, after}
		ts.parts[t] = p
	}
	if p == [2]string{} {
		return value
	}
	// Style each line on its own, so that a line shown alone keeps its
	// colour.
	return p[0] + strings.ReplaceAll(value, "\n", p[1]+"\n"+p[0]) + p[1]
}

// Highlighter highlights a whole source with the same colours as a
// [View], so tokens that span several lines get the correct colour. A
// background goroutine highlights the source until it reaches the end or
// [Highlighter.Stop] is called. A source longer than maxHighlightFile, or
// with a line longer than maxHighlightLine, is highlighted one line at a
// time instead, and its long lines are plain. Lexing a source takes at
// most lexBudget in all, and the lines that it has not reached by then
// are plain. [Highlighter.Line] waits a little for its line, and returns
// the line plain if it is not ready by then. While [Highlighter.Busy]
// reports true, such lines can get their colours, so the screen should be
// drawn again. The methods are safe for concurrent use.
type Highlighter struct {
	mu      sync.Mutex
	grew    chan struct{} // closed, and replaced, when lexing adds lines or ends
	lines   []string      // the lines highlighted so far
	done    bool          // lines holds every line
	stop    atomic.Bool   // Stop has asked the background goroutine to end
	running bool          // the background goroutine is running
	// Line has given up waiting for line lateUntil-1, so it does not wait
	// again until lexing gets past that line.
	lateUntil int

	// plain holds the lines of the source, for a line that is not ready
	// and for lexing line by line.
	plain    []string
	fallback func(string) string

	// The lexer state, used only by the background goroutine.
	lexer   chroma.Lexer    // the lexer for each line, or nil to lex the whole source
	it      chroma.Iterator // the tokens of the whole source, not coalesced
	ts      *tokenStyler
	process func(string) string
	cur     strings.Builder // the start of the line being highlighted
	run     strings.Builder // the text of the tokens of type runType not yet rendered
	runType chroma.TokenType
	src     string        // the source, used by fallback after a panic
	spent   time.Duration // the time spent lexing so far
}

// lexBatch is the number of lines that lexAll adds at a time.
const lexBatch = 256

// lexBudget limits the time that a Highlighter spends lexing its source,
// because some lexers take far longer on some text than its size
// suggests. A typical file of maxHighlightFile bytes takes far less.
const lexBudget = 2 * time.Second

// lineWait is how long Line waits for the background goroutine to reach
// its line. It is short, because Line runs while the screen is drawn and
// so holds up the next key press. The first lexBatch lines of a typical
// file are ready well within it.
const lineWait = 25 * time.Millisecond

// NewHighlighter returns a Highlighter for src, the content of the file at
// path, on the background bg, and starts its background goroutine. It
// passes the text of each token through processValue. If chroma cannot
// lex src, the lines are those of fallback(src). If the lexer panics, the
// lines from the panic onwards are those of fallback(src). A line that is
// not highlighted is fallback(line).
func NewHighlighter(path, src string, bg color.Color, processValue, fallback func(string) string) *Highlighter {
	h := &Highlighter{grew: make(chan struct{}), plain: strings.Split(src, "\n"), fallback: fallback}
	h.ts, h.process = newTokenStyler(bg), processValue
	lexer := fileLexer(path, src)
	if len(src) > maxHighlightFile || slices.ContainsFunc(h.plain, func(l string) bool { return len(l) > maxHighlightLine }) {
		h.lexer = lexer
	} else {
		// The whole source is lexed in the background, so a slow token
		// does not hold up the screen. The lexer is not coalesced, so that
		// Stop and lexBudget take effect between any two tokens.
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

// start starts the background goroutine. h.mu must be held, or h must not
// yet be shared.
func (h *Highlighter) start() {
	h.running = true
	h.stop.Store(false)
	go h.lexAll(len(h.lines))
}

// lexAll highlights the source from line n until it reaches the end or
// Stop is called. It runs in its own goroutine.
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

// lex highlights up to n more lines and returns them. done is the number
// of lines already highlighted. eof is true if lex reached the end of the
// source, and then the last line returned holds the rest of the source.
// lex returns early, with the lines so far, if Stop is called. Once lexing
// has taken lexBudget, the remaining lines are plain.
func (h *Highlighter) lex(done, n int) (lines []string, eof bool) {
	start := time.Now()
	defer func() { h.spent += time.Since(start) }()
	overBudget := func() bool { return h.spent+time.Since(start) > lexBudget }
	if h.lexer != nil {
		return h.lexLines(done, n, overBudget)
	}
	return h.lexTokens(done, n, overBudget)
}

// lexLines is lex for a source that is highlighted one line at a time.
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

// lexTokens is lex for a source that is highlighted as a whole. It reads
// tokens until it has n more lines.
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
			// Like chroma.Coalesce, render adjacent tokens of the same
			// type together.
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

// addRun renders the tokens in h.run, appends the lines that they end to
// lines, and keeps the start of the next line in h.cur.
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

// plainFrom returns the lines of the source from the 0-based line i on,
// as fallback gives them, and ends lexing.
func (h *Highlighter) plainFrom(i int) []string {
	all := strings.Split(h.fallback(h.src), "\n")
	h.it, h.cur, h.run, h.src = nil, strings.Builder{}, strings.Builder{}, ""
	return all[min(i, len(all)):]
}

// lexLine highlights the source line l on its own. It returns fallback(l)
// if l is longer than maxHighlightLine or the lexer fails.
func (h *Highlighter) lexLine(l string) string {
	if len(l) <= maxHighlightLine {
		value := func(v string) string { return h.process(strings.TrimRight(v, "\n")) }
		if out, ok := highlightLine(h.lexer, l, h.ts, value); ok {
			return out
		}
	}
	return h.fallback(l)
}

// next returns the next token. If the lexer panics, next recovers and
// returns ok false.
func (h *Highlighter) next() (token chroma.Token, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return h.it(), true
}

// Line returns the 0-based line i of the highlighted output, as
// strings.Split would split the whole output. ok is false if i is past the
// last line. If the background goroutine has not reached the line within
// lineWait, Line returns it plain, with final false, and does not wait
// again until the goroutine has passed it. A later call returns the line
// highlighted. After Stop, Line starts the goroutine again.
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

// Busy reports whether the background goroutine is still highlighting,
// so that lines that Line returned plain can get their colours.
func (h *Highlighter) Busy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running && !h.done
}

// Stop ends the background goroutine after its current token or line. A
// later call to Line starts it again.
func (h *Highlighter) Stop() { h.stop.Store(true) }

// ansiFor returns the ANSI palette colour for a token type. If ok is
// false, the token uses the terminal's default foreground.
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

// renderLines styles each line of value separately and joins them with
// plain newlines. This avoids the padding that Lip Gloss adds to a
// multi-line string, for example a chroma CommentSingle token, which ends
// with a newline.
func renderLines(s lipgloss.Style, value string) string {
	return EachLine(value, func(l string) string { return s.Render(l) })
}
