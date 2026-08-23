package cloud

import (
	"context"
	"log"
	"time"
)

// TickInterval is how often the scheduler asks whether a sync is owed.
// A minute is fine granularity for schedules measured in hours and days, and
// the check is one small file read when nothing is due.
const TickInterval = time.Minute

// Scheduler runs due syncs in the background. It is deliberately a poller
// rather than a timer per schedule: `next_run_at` lives in the settings file,
// so a server that was switched off over its deadline simply finds the run
// overdue on the first tick after it comes back — no catch-up bookkeeping,
// and no timer to rebuild when the user changes the frequency.
type Scheduler struct {
	Service  *Service
	Interval time.Duration
	// Log receives one line per run. Nil silences it (tests).
	Log *log.Logger
}

// Start runs the scheduler until ctx is cancelled. It returns immediately;
// the loop owns its own goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	interval := s.Interval
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
				s.Tick(ctx)
			}
		}
	}()
}

// Tick runs one scheduling check. Exported so a test can drive the loop
// deterministically instead of waiting on a ticker.
func (s *Scheduler) Tick(ctx context.Context) {
	ran, err := s.Service.RunIfDue(ctx)
	if err != nil {
		// The failure is already recorded in the settings (the UI shows it);
		// this is for whoever is reading the server's log.
		s.logf("[thoughtmesh] scheduled cloud sync failed: %v", err)
		return
	}
	if ran {
		set, err := s.Service.Settings()
		if err != nil || set.LastFileName == nil {
			s.logf("[thoughtmesh] scheduled cloud sync uploaded")
			return
		}
		s.logf("[thoughtmesh] scheduled cloud sync uploaded %s", *set.LastFileName)
	}
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.Log == nil {
		return
	}
	s.Log.Printf(format, args...)
}
