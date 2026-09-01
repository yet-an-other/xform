package roster_test

import (
	"context"
	"testing"
	"time"
)

// Start must return: every Start in the panel (caches, watched sources)
// spawns its own loop, and main relies on that. v1.2.0 shipped a blocking
// Start — the panel process never reached ListenAndServe and listened on
// nothing.
func TestStartReturns(t *testing.T) {
	h := newHarness(t)
	returned := make(chan struct{})
	go func() {
		h.service.Start(context.Background())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Start blocked: it must spawn its apply loop and return")
	}
}
