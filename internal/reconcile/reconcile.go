// Package reconcile turns xray's raw cumulative traffic counters — which
// reset on every xray restart — into durable totals and speed estimates
// (SPEC.md §3 counter reconciliation).
package reconcile

import "time"

// deltaSample is one poll's reconciled counter delta with its elapsed window.
type deltaSample struct {
	bytes   uint64
	elapsed time.Duration
}

// Tracker reconciles one raw counter direction. Not safe for concurrent use;
// callers serialize their polls.
type Tracker struct {
	creditBaseline bool
	seen           bool
	lastRaw        uint64
	lastDelta      uint64
	total          uint64
	window         [2]deltaSample // ring of the most recent deltas
	samples        int
}

// NewTracker starts a reconciliation. creditBaseline controls whether the
// first observed raw value counts toward the total: true for display totals
// (the xray row shows xray's current epoch immediately); false for durable
// per-user totals, whose SQLite rows already hold earlier epochs — crediting
// again after a panel restart would double-count.
func NewTracker(creditBaseline bool) *Tracker {
	return &Tracker{creditBaseline: creditBaseline}
}

// Add folds one raw reading into the total and speed window. raw < lastRaw
// means xray restarted and the counter reset — raw itself is the delta
// (SPEC.md §3). Traffic between the last poll and a restart is unknowable.
func (t *Tracker) Add(raw uint64, elapsed time.Duration) {
	if !t.seen {
		// Baseline: without credit the total only grows from here; crediting
		// the baseline never yields a speed sample either way — spreading
		// lifetime traffic over one poll would spike the estimate.
		t.seen, t.lastRaw = true, raw
		if t.creditBaseline {
			t.total = raw
		}
		return
	}
	delta := raw
	if raw >= t.lastRaw {
		delta = raw - t.lastRaw
	}
	t.lastRaw = raw
	t.total += delta
	t.lastDelta = delta
	t.window[t.samples%2] = deltaSample{bytes: delta, elapsed: elapsed}
	t.samples++
}

// Total is the reconciled durable total.
func (t *Tracker) Total() uint64 { return t.total }

// LastDelta is the most recent poll's reconciled delta (0 on the baseline
// poll) — what a durable store applies per poll.
func (t *Tracker) LastDelta() uint64 { return t.lastDelta }

// Speed is the mean of the last 2 deltas ÷ interval (SPEC.md §3).
func (t *Tracker) Speed() uint64 {
	if t.samples == 0 {
		return 0
	}
	var bytes uint64
	var elapsed time.Duration
	for i := max(t.samples-2, 0); i < t.samples; i++ {
		bytes += t.window[i%2].bytes
		elapsed += t.window[i%2].elapsed
	}
	if elapsed <= 0 {
		return 0
	}
	return uint64(float64(bytes) / elapsed.Seconds())
}
