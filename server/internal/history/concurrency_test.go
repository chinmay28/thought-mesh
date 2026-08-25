package history

import (
	"fmt"
	"sync"
	"testing"
)

// Three callers reach the repository at once — the watcher goroutine, the sync
// scheduler, and any HTTP handler — and they all stage into the same index.
// Unserialized, git refuses the collision ("Unable to create index.lock"),
// which is a lost commit and an error somebody has to read.
func TestConcurrentCallersDoNotCollide(t *testing.T) {
	r, root := newRepo(t)

	const writers = 6
	var wg sync.WaitGroup
	errs := make(chan error, writers*3)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			write(t, root, fmt.Sprintf("Note%d.md", n), fmt.Sprintf("written by %d\n", n))
			if _, err := r.Commit(fmt.Sprintf("Notes edited %d", n), "", KindEdit); err != nil {
				errs <- err
			}
			// Readers and the watcher's fingerprint run against the same index.
			if _, err := r.Fingerprint(); err != nil {
				errs <- err
			}
			if _, err := r.Log(10); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent git use failed: %v", err)
	}

	// Every note made it in, whichever order the commits landed in.
	commits, err := r.Log(50)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	for i := 0; i < writers; i++ {
		name := fmt.Sprintf("Note%d.md", i)
		if _, err := r.Show(commits[0].Ref, name); err != nil {
			t.Errorf("%s is not in the newest commit: %v", name, err)
		}
	}
}
