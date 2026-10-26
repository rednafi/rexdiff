package vcs

import "time"

// SetClock sets the clock of w. The pause between two checks follows it.
// A clock that does not move turns off the pause.
func (w *Watcher) SetClock(now func() time.Time) { w.now = now }
