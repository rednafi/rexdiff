// Package search finds the files of the search screen, and the lines of
// the find band. The limits and the smart case come from herdr-reviewr.
package search

import (
	"slices"

	"github.com/sahilm/fuzzy"
)

// The limits of the results. A "… more" row tells that there are others.
const (
	// MaxFiles is the largest number of files that the search shows.
	MaxFiles = 50
	// MaxCodeLines is the largest number of matching lines that the search
	// shows.
	MaxCodeLines = 200
)

// Files returns at most limit of paths that match q, the best match
// first, and the number of all the matches. For an empty q, the paths in
// recent come first, in their order, and then the other paths.
// herdr-reviewr ranks the files of an empty query by use. rexdiff keeps
// no history, so recent has the picks of this session.
func Files(paths, recent []string, q string, limit int) (hits []string, total int) {
	if q == "" {
		for _, p := range recent {
			if slices.Contains(paths, p) && !slices.Contains(hits, p) {
				hits = append(hits, p)
			}
		}
		total = len(paths)
		for _, p := range paths {
			if len(hits) >= limit {
				break
			}
			if !slices.Contains(hits, p) {
				hits = append(hits, p)
			}
		}
		return hits[:min(len(hits), limit)], total
	}
	ms := fuzzy.Find(q, paths)
	for _, m := range ms[:min(len(ms), limit)] {
		hits = append(hits, m.Str)
	}
	return hits, len(ms)
}

// Indices returns the indices of the labels that match q, the best match
// first. The match is the same fuzzy match as in Files.
func Indices(q string, labels []string) []int {
	ms := fuzzy.Find(q, labels)
	is := make([]int, len(ms))
	for i, m := range ms {
		is[i] = m.Index
	}
	return is
}
