package filesource

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// parsed is a test source's value. Generation bumps only when the text
// actually changes — the same shape as the xray config's roster version, and
// what proves the previous value reaches the parse.
type parsed struct {
	Text       string
	Generation uint64
}

const testParseFailed Reason = "parse_failed"

var testMessages = Messages{
	ReadFailed:      {Fresh: "unreadable", Stale: "unreadable; last valid kept"},
	testParseFailed: {Fresh: "unparsable", Stale: "unparsable; last valid kept"},
}

// parses counts how often a watcher's parse ran, so the debounce can be
// observed rather than inferred.
type parses struct{ count atomic.Int64 }

func (p *parses) parse(previous parsed, document []byte) (parsed, Reason, error) {
	p.count.Add(1)
	text := strings.TrimSpace(string(document))
	if strings.Contains(text, "bad") {
		return parsed{}, testParseFailed, errors.New("bad document")
	}
	next := parsed{Text: text, Generation: previous.Generation}
	if text != previous.Text {
		next.Generation++
	}
	return next, "", nil
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(document []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(document)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// waitFor polls until condition holds or the deadline passes — a watched
// source is asynchronous by design (fsnotify + debounce).
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// start builds a watcher with a debounce short enough that the suite does not
// spend its time waiting. TestWatcherCoalescesABurstOfSaves keeps the real
// window, because that is the one behaviour the window exists for.
func start(t *testing.T, path string, counter *parses) *Watcher[parsed] {
	t.Helper()
	watcher := New(path, "test source", counter.parse, testMessages)
	watcher.debounce = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)
	return watcher
}

func TestWatcherLoadsAndPicksUpEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeFile(t, path, "first")

	watcher := start(t, path, &parses{})

	initial := watcher.Snapshot()
	if !initial.Configured() || !initial.Available() || initial.Stale || initial.Error != nil {
		t.Fatalf("initial snapshot = %+v, want a current first load", initial)
	}
	if initial.Value.Text != "first" {
		t.Fatalf("initial value = %q, want %q", initial.Value.Text, "first")
	}

	writeFile(t, path, "second")
	waitFor(t, "the edited document", func() bool {
		return watcher.Snapshot().Value.Text == "second"
	})
}

// The previous value reaches the parse, so a parse can derive state that has
// to move in the same step as the swap.
func TestParseReceivesThePreviousValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeFile(t, path, "first")

	watcher := start(t, path, &parses{})
	if got := watcher.Snapshot().Value.Generation; got != 1 {
		t.Fatalf("first generation = %d, want 1", got)
	}

	writeFile(t, path, "second")
	waitFor(t, "the second document", func() bool {
		return watcher.Snapshot().Value.Text == "second"
	})
	if got := watcher.Snapshot().Value.Generation; got != 2 {
		t.Errorf("generation after a real change = %d, want 2", got)
	}

	// Rewriting identical content still fires a change; the parse sees the
	// previous value and declines to move the generation.
	writeFile(t, path, "second")
	time.Sleep(120 * time.Millisecond)
	if got := watcher.Snapshot().Value.Generation; got != 2 {
		t.Errorf("generation after an identical rewrite = %d, want an unchanged 2", got)
	}
}

// A single save produces a burst of events (write + chmod, or temp-file
// rename). The debounce absorbs them into one re-parse, which is the only
// reason the window exists — so this test keeps the production window.
func TestWatcherCoalescesABurstOfSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeFile(t, path, "first")

	counter := &parses{}
	watcher := New(path, "test source", counter.parse, testMessages)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	for index := range 5 {
		writeFile(t, path, "burst-"+string(rune('a'+index)))
	}
	waitFor(t, "the last write of the burst", func() bool {
		return watcher.Snapshot().Value.Text == "burst-e"
	})
	// Outlast a debounce that might still be armed, then count.
	time.Sleep(2 * defaultDebounce)

	if got := counter.count.Load(); got != 2 {
		t.Errorf("parses = %d, want 2 (the initial load and one coalesced re-parse)", got)
	}
}

// Editors and config managers often replace the file atomically (write temp +
// rename). The watcher follows the path, not the inode.
func TestWatcherFollowsAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	writeFile(t, path, "first")

	watcher := start(t, path, &parses{})

	temp := filepath.Join(dir, ".source.txt.tmp")
	writeFile(t, temp, "replaced")
	if err := os.Rename(temp, path); err != nil {
		t.Fatalf("rename: %v", err)
	}
	waitFor(t, "the replaced document", func() bool {
		return watcher.Snapshot().Value.Text == "replaced"
	})
}

// The watcher watches the parent directory, so it must ignore everything in
// it but its own path.
func TestWatcherIgnoresSiblingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	writeFile(t, path, "first")

	counter := &parses{}
	watcher := start(t, path, counter)

	writeFile(t, filepath.Join(dir, "sibling.txt"), "unrelated")
	time.Sleep(120 * time.Millisecond)

	if got := counter.count.Load(); got != 1 {
		t.Errorf("parses = %d after a sibling write, want only the initial load", got)
	}
	if got := watcher.Snapshot().Value.Text; got != "first" {
		t.Errorf("value = %q after a sibling write, want %q", got, "first")
	}
}

// A half-saved document must not empty the source: the last valid value stays,
// marked stale, and recovers on the next good save.
func TestWatcherRetainsLastValidAfterParseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeFile(t, path, "first")
	loadedAt := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)

	counter := &parses{}
	watcher := New(path, "test source", counter.parse, testMessages).
		WithClock(func() time.Time { return loadedAt })
	watcher.debounce = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	writeFile(t, path, "bad")
	waitFor(t, "the stale snapshot", func() bool {
		return watcher.Snapshot().Stale
	})

	stale := watcher.Snapshot()
	if !stale.Available() || stale.Value.Text != "first" || !stale.LoadedAt.Equal(loadedAt) {
		t.Errorf("stale snapshot = %+v, want the retained first load", stale)
	}
	if stale.Error == nil || stale.Error.Reason != testParseFailed || stale.Error.Message != "unparsable; last valid kept" {
		t.Errorf("stale error = %+v, want the parse's stale message", stale.Error)
	}

	writeFile(t, path, "recovered")
	waitFor(t, "recovery", func() bool {
		snapshot := watcher.Snapshot()
		return !snapshot.Stale && snapshot.Value.Text == "recovered"
	})
	if got := watcher.Snapshot().Error; got != nil {
		t.Errorf("recovered error = %+v, want none", got)
	}
}

// A file that goes away is a read failure, and reads the same way: the last
// valid value stays and is marked stale.
func TestWatcherRetainsLastValidAfterReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeFile(t, path, "first")

	watcher := start(t, path, &parses{})

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	waitFor(t, "the stale read failure", func() bool {
		snapshot := watcher.Snapshot()
		return snapshot.Stale && snapshot.Error != nil && snapshot.Error.Reason == ReadFailed
	})

	stale := watcher.Snapshot()
	if !stale.Available() || stale.Value.Text != "first" {
		t.Errorf("stale snapshot = %+v, want the retained first load", stale)
	}
	if stale.Error.Message != "unreadable; last valid kept" {
		t.Errorf("stale message = %q, want the stale rendering", stale.Error.Message)
	}
}

// A source that has never loaded has nothing to retain: it reports the fresh
// message, and is not stale — there is no last valid value to be stale about.
func TestWatcherWithoutAReadableFileIsUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	loadedAt := time.Date(2026, time.August, 29, 9, 45, 0, 0, time.UTC)

	counter := &parses{}
	watcher := New(path, "test source", counter.parse, testMessages).
		WithClock(func() time.Time { return loadedAt })
	watcher.debounce = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	missing := watcher.Snapshot()
	if !missing.Configured() || missing.Available() || missing.Stale || !missing.LoadedAt.IsZero() {
		t.Errorf("never-loaded snapshot = %+v, want configured but unavailable", missing)
	}
	if missing.Error == nil || missing.Error.Reason != ReadFailed || missing.Error.Message != "unreadable" {
		t.Errorf("never-loaded error = %+v, want the fresh read_failed message", missing.Error)
	}

	// A file that appears later is picked up without a restart.
	writeFile(t, path, "arrived")
	waitFor(t, "the appearing file", func() bool {
		return watcher.Snapshot().Available()
	})
	arrived := watcher.Snapshot()
	if arrived.Stale || arrived.Error != nil || !arrived.LoadedAt.Equal(loadedAt) || arrived.Value.Text != "arrived" {
		t.Errorf("first successful snapshot = %+v, want a current load", arrived)
	}
}

// An empty path is an unconfigured source: no read is attempted, so there is
// no failure to report.
func TestWatcherWithoutAPathReadsNothing(t *testing.T) {
	counter := &parses{}
	watcher := New("", "test source", counter.parse, testMessages)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	snapshot := watcher.Snapshot()
	if snapshot.Configured() || snapshot.Available() || snapshot.Stale || snapshot.Error != nil {
		t.Errorf("unconfigured snapshot = %+v, want unconfigured and unavailable without error", snapshot)
	}
	if got := counter.count.Load(); got != 0 {
		t.Errorf("parses = %d for an unconfigured source, want 0", got)
	}
}

// Subscribers hear about successful loads only — a failed re-parse published
// as a change would tell a subscriber to go read something that did not move.
func TestWatcherPublishesOnlySuccessfulLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeFile(t, path, "first")

	counter := &parses{}
	watcher := New(path, "test source", counter.parse, testMessages)
	watcher.debounce = 5 * time.Millisecond
	changes := watcher.Changes()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first load's notification")
	}

	writeFile(t, path, "bad")
	select {
	case <-changes:
		t.Fatal("received a change for a failed parse")
	case <-time.After(200 * time.Millisecond):
	}

	writeFile(t, path, "second")
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the successful load's notification")
	}
}

// A subscriber that is busy must not block the reload, and must not be handed
// a queue of notifications it can do nothing with: they coalesce into one.
func TestChangesCoalesceForABusySubscriber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeFile(t, path, "first")

	counter := &parses{}
	watcher := New(path, "test source", counter.parse, testMessages)
	watcher.debounce = 5 * time.Millisecond
	changes := watcher.Changes()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	for _, content := range []string{"second", "third", "fourth"} {
		writeFile(t, path, content)
		waitFor(t, "the "+content+" load", func() bool {
			return watcher.Snapshot().Value.Text == content
		})
	}

	// Four successful loads, one never-drained subscriber: one notification.
	drained := 0
	for {
		select {
		case <-changes:
			drained++
			continue
		default:
		}
		break
	}
	if drained != 1 {
		t.Errorf("buffered notifications = %d, want 1 coalesced", drained)
	}
}

// A persistent failure logs when it starts and when it clears, not on every
// save attempt — the panel polls its sources, and a broken file would
// otherwise fill the journal.
func TestWatcherLogsAFailureOncePerTransition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeFile(t, path, "first")

	var logs lockedBuffer
	counter := &parses{}
	watcher := New(path, "test source", counter.parse, testMessages).
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	watcher.debounce = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	writeFile(t, path, "bad")
	waitFor(t, "the first failure", func() bool {
		return strings.Contains(logs.String(), "cannot load test source")
	})

	// The same failure again: the file changed, the outcome did not.
	writeFile(t, path, "bad")
	time.Sleep(120 * time.Millisecond)
	if got := strings.Count(logs.String(), "cannot load test source"); got != 1 {
		t.Errorf("failure logs = %d while the failure persisted, want 1; logs: %s", got, logs.String())
	}

	writeFile(t, path, "recovered")
	waitFor(t, "the recovery log", func() bool {
		return strings.Contains(logs.String(), "test source load recovered")
	})

	writeFile(t, path, "bad")
	waitFor(t, "the failure after recovery", func() bool {
		return strings.Count(logs.String(), "cannot load test source") == 2
	})
}

// The reason and the diagnostic stay in the panel's own logs; only the table's
// wording reaches a consumer.
func TestFailureMessagesComeFromTheParsesTable(t *testing.T) {
	if got := testMessages.message(ReadFailed, false); got != "unreadable" {
		t.Errorf("fresh read message = %q", got)
	}
	if got := testMessages.message(ReadFailed, true); got != "unreadable; last valid kept" {
		t.Errorf("stale read message = %q", got)
	}
	if got := testMessages.message(Reason("unlisted"), false); got != "" {
		t.Errorf("unlisted reason message = %q, want empty", got)
	}
}
