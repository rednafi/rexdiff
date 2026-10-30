package vcs

import "time"

// SetClock replaces the clock of w. A clock that never moves turns the
// pause off.
func (w *Watcher) SetClock(now func() time.Time) { w.now = now }

// SetMaxDiffBytes replaces the 128 MB diff limit, which makes a test slow.
func (r *Repo) SetMaxDiffBytes(n int) { r.maxDiffBytes = n }
