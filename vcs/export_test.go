package vcs

import "time"

// SetClock replaces the clock that w uses to time the pause between
// checks. A clock that never moves turns the pause off.
func (w *Watcher) SetClock(now func() time.Time) { w.now = now }

// SetMaxDiffBytes replaces the most text that r.Diff reads, 128 MB, which
// would make a test slow.
func (r *Repo) SetMaxDiffBytes(n int) { r.maxDiffBytes = n }
