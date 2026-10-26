// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"cmp"
	"image/color"
	"io"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Cache keeps highlighted lines, so that chroma highlights each line once.
// Views that share a Cache reuse its lines and its last lexer. The zero
// value is an empty Cache. A Cache is not safe for concurrent use.
type Cache struct {
	lines map[syntaxKey]string
	size  int // the bytes of the lines and their sources

	// lexer keeps the last lexer, because to select a lexer from the
	// content is slow.
	lexer struct {
		path, content string
		l             chroma.Lexer
	}
}

// maxCacheSize limits the bytes of the lines and the sources in a Cache.
// If a put goes above this size, put empties the Cache first.
const maxCacheSize = 32 << 20

// put keeps the highlighted line s for key k.
func (c *Cache) put(k syntaxKey, s string) {
	// k.source is a part of the file string. A copy lets the cache keep
	// the line without the whole file.
	k.source = strings.Clone(k.source)
	n := len(k.source) + len(s)
	if c.lines == nil || c.size+n > maxCacheSize {
		c.lines, c.size = make(map[syntaxKey]string), 0
	}
	c.lines[k] = s
	c.size += n
}

// syntaxKey is the key of a [Cache]. It has the lexer, the source, and the
// background colour of the source. noColor is true for no background, because
// lipgloss.NoColor has the same RGBA value as black.
type syntaxKey struct {
	lexer, source string
	noColor       bool
	r, g, b, a    uint32
}

// newSyntaxKey returns the key of source for the lexer named lexer, on
// bgColor.
func newSyntaxKey(lexer, source string, bgColor color.Color) syntaxKey {
	k := syntaxKey{lexer: lexer, source: source}
	if _, none := bgColor.(lipgloss.NoColor); bgColor == nil || none {
		k.noColor = true
		return k
	}
	k.r, k.g, k.b, k.a = bgColor.RGBA()
	return k
}

// highlightCode returns source with syntax colours on the background
// bgColor. The cache keeps the result. If chroma fails, highlightCode
// returns source as it is.
func (v *View) highlightCode(source string, bgColor color.Color) string {
	// Chroma makes a lone CR a line break. Thus show each CR as a symbol
	// first.
	source = strings.ReplaceAll(source, "\r", "\u240d")
	lexer := v.chromaLexer()
	key := newSyntaxKey(lexer.Config().Name, source, bgColor)
	if cached, ok := v.cache.lines[key]; ok {
		return cached
	}
	it, err := lexer.Tokenise(nil, source)
	if err != nil {
		return source
	}
	var b strings.Builder
	f := ANSIFormatter(bgColor, processChromaValue)
	if err := f.Format(&b, nil, it); err != nil {
		return source
	}
	out := b.String()
	v.cache.put(key, out)
	return out
}

// processChromaValue escapes the text of a token for the terminal. A code
// line has no line break. Thus processChromaValue removes the trailing line
// breaks.
func processChromaValue(value string) string {
	return Escape(strings.TrimRight(value, "\n"))
}

// chromaLexer returns the lexer of the old file, or of the new file if
// the old file is empty, and keeps it in the cache.
func (v *View) chromaLexer() chroma.Lexer {
	lx := &v.cache.lexer
	content := cmp.Or(v.before, v.after)
	if lx.l == nil || lx.path != v.path || lx.content != content {
		lx.path, lx.content = v.path, content
		lx.l = Lexer(lx.path, lx.content)
	}
	return lx.l
}

// Lexer returns the coalesced lexer for the file at path. If no lexer
// matches the name, it selects one from the content. If no lexer matches
// the content, it returns the fallback lexer.
func Lexer(path, content string) chroma.Lexer {
	// matchLexer caches the lexer, and the lexer is already coalesced.
	if l := matchLexer(path); l != nil {
		return l
	}
	l := lexers.Analyse(content)
	if l == nil {
		l = lexers.Fallback
	}
	return chroma.Coalesce(l)
}

// lexerCache keeps the lexer of each file name, or nil if none matches. To
// match a name is slow, because lexers.Match tests the patterns of each
// lexer. The mutex makes the cache safe for more than one goroutine.
var (
	lexerCacheMu sync.RWMutex
	lexerCache   = map[string]chroma.Lexer{}
)

// matchLexer returns the coalesced chroma lexer for the file name, or nil if
// no lexer matches. The lexer is safe to use again.
func matchLexer(fileName string) chroma.Lexer {
	lexerCacheMu.RLock()
	l, ok := lexerCache[fileName]
	lexerCacheMu.RUnlock()
	if ok {
		return l
	}
	if l = lexers.Match(fileName); l != nil {
		l = chroma.Coalesce(l)
	}
	lexerCacheMu.Lock()
	lexerCache[fileName] = l
	lexerCacheMu.Unlock()
	return l
}

// ANSIFormatter returns a chroma formatter that colours the tokens with the
// ANSI palette colours, on the background bgColor. It ignores the chroma
// style. It calls processValue on the text of each token first.
func ANSIFormatter(bgColor color.Color, processValue func(string) string) chroma.Formatter {
	format := func(w io.Writer, _ *chroma.Style, it chroma.Iterator) error {
		plain := lipgloss.NewStyle().Background(bgColor)
		for token := it(); token != chroma.EOF; token = it() {
			value := processValue(token.Value)
			s := plain
			if c, italic, ok := ansiFor(token.Type); ok {
				s = plain.Foreground(lipgloss.Color(c)).Italic(italic)
			}
			if _, err := io.WriteString(w, renderLines(s, value)); err != nil {
				return err
			}
		}
		return nil
	}
	return chroma.FormatterFunc(format)
}

// ansiFor returns the ANSI palette colour of a token type. If ok is false,
// the token has the default foreground of the terminal.
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

// renderLines styles each line of value alone, and joins them with plain
// newlines. A token can contain newlines. For example, a chroma
// CommentSingle ends with one. Lip Gloss pads all lines of a multi-line
// string to one width. Without renderLines, this padding adds spaces to
// the line after a comment.
func renderLines(s lipgloss.Style, value string) string {
	if !strings.Contains(value, "\n") {
		return s.Render(value)
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = s.Render(line)
	}
	return strings.Join(lines, "\n")
}
