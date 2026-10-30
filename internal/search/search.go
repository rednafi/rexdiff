// Package search finds files for the search screen and lines for the find
// bar.
package search

import "github.com/sahilm/fuzzy"

// Limits on the number of results.
const (
	MaxFiles     = 50
	MaxGrepLines = 200
)

// Files returns up to limit paths that match query and the total number of
// matches. If query is empty, recent paths come first.
func Files(paths, recent []string, query string, limit int) (hits []string, total int) {
	if query == "" {
		return recentFirst(paths, recent, limit), len(paths)
	}
	ms := fuzzy.Find(query, paths)
	for _, m := range ms[:min(len(ms), limit)] {
		hits = append(hits, m.Str)
	}
	return hits, len(ms)
}

// Indices returns the indices of the labels that match query, best first.
func Indices(query string, labels []string) []int {
	ms := fuzzy.Find(query, labels)
	indices := make([]int, len(ms))
	for i, m := range ms {
		indices[i] = m.Index
	}
	return indices
}

func recentFirst(paths, recent []string, limit int) []string {
	isRecent := make(map[string]bool, len(recent))
	for _, p := range recent {
		isRecent[p] = true
	}
	found := make(map[string]bool, len(recent))
	var others []string
	for _, p := range paths {
		if len(found) == len(isRecent) && len(others) >= limit {
			break
		}
		switch {
		case isRecent[p]:
			found[p] = true
		case len(others) < limit:
			others = append(others, p)
		}
	}
	var hits []string
	for _, p := range recent {
		if found[p] {
			hits = append(hits, p)
			delete(found, p) // a path listed twice in recent appears once
		}
	}
	hits = append(hits, others...)
	return hits[:min(len(hits), limit)]
}
