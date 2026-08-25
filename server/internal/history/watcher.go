package history

import (
	"context"
	"log"
	"time"
)

// TickInterval is how often the watcher looks at the vault. A minute is fine
// granularity for a record of what a note said, and the check is one tree hash
// when nothing has changed.
const TickInterval = time.Minute

// Watcher commits the vault once editing has settled.
//
// Without it, history would exist only where cloud sync does — and sync is off
// until somebody registers a Dropbox app, which would make "your notes are
// versioned" true only for the people who least needed telling. Writing is the
// thing this app is for; writing alone should build the history.
//
// The rule is "changed, then stopped changing", not "changed": a commit per
// keystroke would bury the log, and a commit per tick would cut sentences in
// half. So each tick takes the hash of the tree the vault would commit to, and
// commits only when that hash is unchanged since the previous tick and differs
// from what is already committed. With a one-minute tick, a note reaches the
// history within about two minutes of the writer stopping.
//
// It is a poller rather than a filesystem watcher on purpose, and for the same
// reason the link index is: the vault is edited behind the server's back — by
// git, by Syncthing, by another editor — and a poller notices all of it without
// a platform-specific API or a descriptor per folder.
type Watcher struct {
	Repo     *Repo
	Interval time.Duration
	// Log receives one line per commit. Nil silences it (tests).
	Log *log.Logger

	// last is the previous tick's fingerprint — the other half of "settled".
	last string
	// seen is whether `last` holds a real reading yet. The first tick only
	// takes a baseline, so a server that starts while a note is half-written
	// waits a tick rather than committing the half.
	seen bool
}

// Start runs the watcher until ctx is cancelled. It returns immediately; the
// loop owns its own goroutine.
func (w *Watcher) Start(ctx context.Context) {
	if !w.Repo.Available() {
		return
	}
	interval := w.Interval
	if interval <= 0 {
		interval = TickInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.Tick()
			}
		}
	}()
}

// Tick runs one check. Exported so a test can drive the loop deterministically
// instead of waiting on a ticker.
//
// It reports whether it committed, which is what the test asserts on and what
// the log line is worth writing for.
func (w *Watcher) Tick() bool {
	if !w.Repo.Available() {
		return false
	}
	current, err := w.Repo.Fingerprint()
	if err != nil {
		w.logf("[thoughtmesh] vault history: %v", err)
		return false
	}
	previous, seen := w.last, w.seen
	w.last, w.seen = current, true
	if !seen || current != previous {
		// Either the first look, or the vault moved since the last one —
		// somebody is still writing. Wait for it to settle.
		//
		// A sync or a rollback committing under the watcher lands here too: the
		// tree it sees has moved, so it skips a tick and then finds nothing
		// staged. It costs a minute of latency and never a duplicate commit,
		// which is why nothing has to tell the watcher those happened.
		return false
	}
	made, err := w.Repo.Commit("Notes edited at "+w.Repo.Stamp(), "", KindEdit)
	if err != nil {
		w.logf("[thoughtmesh] vault history: %v", err)
		return false
	}
	if made {
		w.logf("[thoughtmesh] vault history: committed the last few minutes of edits")
	}
	return made
}

func (w *Watcher) logf(format string, args ...any) {
	if w.Log == nil {
		return
	}
	w.Log.Printf(format, args...)
}
