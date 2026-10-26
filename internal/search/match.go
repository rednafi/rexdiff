package search

import (
	"strings"
	"unicode"
)

// Match reports whether text has query, with smart case as in
// herdr-reviewr. A query with an upper case letter matches only the same
// case. Else Match ignores the case of ASCII letters only. An empty query
// matches nothing.
func Match(text, query string) bool {
	if query == "" {
		return false
	}
	if strings.ContainsFunc(query, unicode.IsUpper) {
		return strings.Contains(text, query)
	}
	for i := 0; i+len(query) <= len(text); i++ {
		if foldEqual(text[i:i+len(query)], query) {
			return true
		}
	}
	return false
}

// foldEqual reports whether a and b, which have the same length, are equal
// when ASCII letters ignore case. The bytes of a UTF-8 character other
// than ASCII never fold, so a byte compare is correct.
func foldEqual(a, b string) bool {
	for i := range len(a) {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

// lowerASCII returns the lower case of the ASCII letter c. It returns
// other bytes as they are.
func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
