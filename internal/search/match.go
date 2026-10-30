package search

import (
	"strings"
	"unicode"
)

// Matches reports whether text contains query, with smart case. It ignores
// case for ASCII letters only.
func Matches(text, query string) bool { return Index(text, query) >= 0 }

// Index returns the byte offset of the first [Matches] match, or -1.
func Index(text, query string) int {
	if query == "" {
		return -1
	}
	if strings.ContainsFunc(query, unicode.IsUpper) {
		return strings.Index(text, query)
	}
	// Lowering ASCII letters keeps the byte offsets.
	return strings.Index(lowerASCII(text), query)
}

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
