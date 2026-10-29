package search

import (
	"strings"
	"unicode"
)

// Matches reports whether text contains query, using smart case. A query
// with an upper-case letter must match case exactly. Otherwise Matches
// ignores case, but only for ASCII letters. An empty query matches
// nothing.
func Matches(text, query string) bool { return Index(text, query) >= 0 }

// Index returns the byte offset of the first match of query in text, as
// [Matches] finds it, or -1 if there is none. It takes time linear in the
// lengths of text and query.
func Index(text, query string) int {
	if query == "" {
		return -1
	}
	if strings.ContainsFunc(query, unicode.IsUpper) {
		return strings.Index(text, query)
	}
	// query has no upper-case letter, so it matches the text with its
	// ASCII letters in lower case. Lowering ASCII letters keeps the byte
	// offsets.
	return strings.Index(lowerASCII(text), query)
}

// lowerASCII returns s with its ASCII letters in lower case. It leaves the
// other bytes as they are.
func lowerASCII(s string) string {
	i := strings.IndexFunc(s, func(r rune) bool { return 'A' <= r && r <= 'Z' })
	if i < 0 {
		return s
	}
	b := []byte(s)
	for k := i; k < len(b); k++ {
		if c := b[k]; 'A' <= c && c <= 'Z' {
			b[k] = c + 'a' - 'A'
		}
	}
	return string(b)
}
