// Package journal collects bounded, point-in-time Log snapshots for the two
// fixed units the Panel is allowed to read: its own service and the
// configured xray service (IN-DEV-SPEC §4.2, §6.4).
//
// The only choice a caller has is which of those two sources to read. Unit
// names, counts, filters, cursors, time ranges, and raw journalctl arguments
// are not accepted from anywhere, because journalctl's --unit= expands globs
// and a fixed argv is not by itself a security boundary
// (docs/research/bounded-journald-access.md §"Unit-name and argument safety").
// The bounds and the process seam are unexported for the same reason.
package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Source is the caller's entire vocabulary: one of two fixed services.
type Source string

const (
	SourcePanel Source = "panel"
	SourceXray  Source = "xray"
)

// PanelUnit is compiled in rather than configured: the Panel's own unit name
// is fixed by the deployment this binary ships with.
const PanelUnit = "xform.service"

// namespace is the dedicated journal namespace the deployment grants the
// Panel read ACLs on, and the only one it ever reads (§5.4).
const namespace = "xform"

// outputFields is journalctl's JSON allowlist: the trusted unit fields behind
// the `unit` column, plus the untrusted client fields the entry exposes.
const outputFields = "__CURSOR,__REALTIME_TIMESTAMP,_SYSTEMD_UNIT,UNIT,OBJECT_SYSTEMD_UNIT,COREDUMP_UNIT,SYSLOG_IDENTIFIER,_PID,PRIORITY,MESSAGE"

// Reason is a stable collection failure, safe to expose to the Dashboard.
type Reason string

const (
	ReasonSnapshotInProgress    Reason = "snapshot_in_progress"
	ReasonJournalctlUnavailable Reason = "journalctl_unavailable"
	ReasonAccessDenied          Reason = "access_denied"
	ReasonTimeout               Reason = "timeout"
	ReasonOutputTooLarge        Reason = "output_too_large"
	ReasonMalformedOutput       Reason = "malformed_output"
	ReasonCommandFailed         Reason = "command_failed"
)

// Error is a collection failure carrying one stable reason.
//
// Detail is a bounded summary for the Panel's own logs. It never contains
// journalctl's stderr or a journal message: those are the very data the
// snapshot exists to bound, and a diagnostic is not a place to leak them
// (§6.4).
type Error struct {
	Reason Reason
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Reason)
	}
	return string(e.Reason) + ": " + e.Detail
}

func failure(reason Reason, format string, args ...any) *Error {
	return &Error{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// Entry is one normalized journal record. Identifier, PID, Priority,
// Message, and MessageEncoding are nil where the record did not carry a
// usable value; MessageTruncated marks journalctl's own oversized-field
// elision (§6.4).
type Entry struct {
	Cursor           string
	TimestampUS      uint64
	Unit             string
	Identifier       *string
	PID              *int
	Priority         *int
	Message          *string
	MessageEncoding  *string
	MessageTruncated bool
}

// Snapshot is one best-effort, point-in-time read — never an atomic journal
// transaction (docs/research §"Snapshot, cursor, and denial semantics").
type Snapshot struct {
	CapturedAt time.Time
	Source     Source
	Unit       string
	Limit      int
	Entries    []Entry
}

// limits are the bounds §6.4 fixes. They are not configurable: a caller that
// could raise the entry count would also be rewriting journalctl's --lines.
type limits struct {
	timeout     time.Duration
	entries     int
	stdoutBytes int64
	stderrBytes int64
}

func contractLimits() limits {
	return limits{
		timeout:     5 * time.Second,
		entries:     500,
		stdoutBytes: 8 << 20,
		stderrBytes: 64 << 10,
	}
}

// Reader collects Log snapshots. One Reader serves the whole Panel, because
// the single process slot it holds is a global bound.
type Reader struct {
	// executable is the journalctl path validated at startup, re-checked
	// before every collection.
	executable string
	// xrayUnit is the canonical unit resolved through systemd at startup.
	xrayUnit string

	limits limits

	// start launches the child; nil uses os/exec. Overridden in tests.
	start func(ctx context.Context, command childCommand) (child, error)
	// now reads the wall clock; nil uses time.Now. Overridden in tests.
	now func() time.Time

	// slot is the one global journalctl process allowance. A collection that
	// cannot take it fails fast rather than queueing, so a slow journal can
	// never pile children up on a proxy host.
	slot chan struct{}
}

// NewReader returns a Reader over an already-validated journalctl path and
// canonical xray unit (§5.5 does that validation at startup).
func NewReader(executable, xrayUnit string) *Reader {
	return &Reader{
		executable: executable,
		xrayUnit:   xrayUnit,
		limits:     contractLimits(),
		slot:       make(chan struct{}, 1),
	}
}

// Collect reads the latest bounded snapshot for one fixed source.
func (r *Reader) Collect(ctx context.Context, source Source) (Snapshot, error) {
	unit, err := r.unitFor(source)
	if err != nil {
		return Snapshot{}, err
	}

	select {
	case r.slot <- struct{}{}:
		defer func() { <-r.slot }()
	default:
		return Snapshot{}, failure(ReasonSnapshotInProgress, "another snapshot is already running")
	}

	// Re-checked every time: a package upgrade or a tampered path between
	// startup and now must degrade this one feature, not the Panel (§5.5).
	if err := ValidateExecutable(r.executable); err != nil {
		return Snapshot{}, failure(ReasonJournalctlUnavailable, "journalctl is no longer usable: %s", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, r.limits.timeout)
	defer cancel()

	process, err := r.startChild(runCtx, childCommand{
		Path: r.executable,
		Args: arguments(unit, r.limits.entries),
		Env:  fixedEnvironment(),
	})
	if err != nil {
		return Snapshot{}, failure(ReasonJournalctlUnavailable, "journalctl did not start")
	}

	// The module owns the deadline rather than leaning on the adapter's own
	// context handling: a child that stops producing output must be ended by
	// the reader itself, whoever started it (§4.2). Kill tolerates arriving
	// after Wait, which is what makes the closing race harmless.
	watching := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			_ = process.Kill()
		case <-watching:
		}
	}()

	entries, collectErr := r.read(process, unit)

	// Whatever happened, the child is ended and reaped before classifying.
	if collectErr != nil {
		_ = process.Kill()
	}
	stderr := r.awaitStderr(runCtx, process)
	waitErr := process.Wait()
	close(watching)

	if reason := classify(ctx, runCtx, collectErr, waitErr, stderr); reason != nil {
		return Snapshot{}, reason
	}

	return Snapshot{
		CapturedAt: r.clock(),
		Source:     source,
		Unit:       unit,
		Limit:      r.limits.entries,
		Entries:    entries,
	}, nil
}

// awaitStderr collects the drained stderr, giving up at the deadline. The
// channel is buffered, so the draining goroutine finishes either way and a
// pipe some other process holds open cannot hang the Panel.
func (r *Reader) awaitStderr(runCtx context.Context, process *runningChild) stderrRead {
	select {
	case stderr := <-process.stderrResult():
		return stderr
	case <-runCtx.Done():
		return stderrRead{}
	}
}

// unitFor maps a source to its fixed unit. An unrecognized source is a
// programming error rather than a collection failure, so it carries no stable
// reason: §6.4's reasons all describe an attempt that actually reached
// journalctl, and this one never does.
func (r *Reader) unitFor(source Source) (string, error) {
	switch source {
	case SourcePanel:
		return PanelUnit, nil
	case SourceXray:
		return r.xrayUnit, nil
	default:
		return "", fmt.Errorf("unknown log source %q", source)
	}
}

func (r *Reader) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Reader) startChild(ctx context.Context, command childCommand) (*runningChild, error) {
	start := r.start
	if start == nil {
		start = startProcess
	}
	process, err := start(ctx, command)
	if err != nil {
		return nil, err
	}
	// stderr drains concurrently: a child blocked writing a full stderr pipe
	// would never exit, and its output is capped rather than buffered whole.
	running := &runningChild{child: process, stderr: make(chan stderrRead, 1)}
	go func() {
		text, oversized := readCapped(process.Stderr(), r.limits.stderrBytes)
		if oversized {
			// Stop the child now. Nothing will read the rest of its stderr,
			// so leaving it alive would block it on a full pipe until the
			// deadline and report that breach as a timeout.
			_ = process.Kill()
		}
		running.stderr <- stderrRead{text: text, oversized: oversized}
	}()
	return running, nil
}

// read decodes the child's stdout as a stream of JSON objects. It never
// buffers the whole output: the byte cap rides on the reader itself, and the
// decoder consumes one object at a time (§6.4).
func (r *Reader) read(process *runningChild, unit string) ([]Entry, error) {
	// One byte past the cap, so a breach is detectable rather than silently
	// truncating the stream into something that parses.
	counter := &countingReader{reader: io.LimitReader(process.Stdout(), r.limits.stdoutBytes+1)}
	decoder := json.NewDecoder(counter)

	entries := make([]Entry, 0, 16)
	for decoder.More() {
		if overflow := r.stdoutOverflow(counter); overflow != nil {
			return nil, overflow
		}
		if len(entries) == r.limits.entries {
			return nil, failure(ReasonMalformedOutput, "journalctl returned more than %d records", r.limits.entries)
		}
		var record map[string]json.RawMessage
		if err := decoder.Decode(&record); err != nil {
			// A cap breach surfaces here as a truncated object; report the
			// breach rather than the decode error it caused.
			if overflow := r.stdoutOverflow(counter); overflow != nil {
				return nil, overflow
			}
			return nil, failure(ReasonMalformedOutput, "record %d is not a JSON object", len(entries)+1)
		}
		entry, err := normalize(record, unit)
		if err != nil {
			return nil, failure(ReasonMalformedOutput, "record %d: %s", len(entries)+1, err)
		}
		entries = append(entries, entry)
	}
	return entries, r.stdoutOverflowError(counter)
}

// stdoutOverflow reports the cap breach, if the reader has passed it.
func (r *Reader) stdoutOverflow(counter *countingReader) *Error {
	if counter.count > r.limits.stdoutBytes {
		return failure(ReasonOutputTooLarge, "stdout exceeded %d bytes", r.limits.stdoutBytes)
	}
	return nil
}

// stdoutOverflowError is stdoutOverflow as a plain error, so a nil result
// stays nil once it reaches an error-typed return.
func (r *Reader) stdoutOverflowError(counter *countingReader) error {
	if overflow := r.stdoutOverflow(counter); overflow != nil {
		return overflow
	}
	return nil
}

// classify picks the one reason to report, in the precedence §6.4 fixes:
// caller cancellation, then denial, timeout, byte caps, malformed output, and
// finally any other non-zero exit.
func classify(callerCtx, runCtx context.Context, collectErr, waitErr error, stderr stderrRead) error {
	// The caller going away is not a collection failure to report; it is
	// their own cancellation coming back.
	if callerErr := callerCtx.Err(); callerErr != nil {
		return callerErr
	}
	// A clean run is a clean run. journalctl may warn about a file it could
	// not open while still returning a complete snapshot, and §6.4 scopes
	// access_denied to a child that failed, not to one that succeeded.
	if collectErr == nil && waitErr == nil && runCtx.Err() == nil && !stderr.oversized {
		return nil
	}
	if deniedAccess(stderr.text) {
		return failure(ReasonAccessDenied, "journalctl could not open the %s namespace", namespace)
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return failure(ReasonTimeout, "journalctl did not finish within the deadline")
	}
	if stderr.oversized {
		return failure(ReasonOutputTooLarge, "stderr exceeded its cap")
	}
	if collectErr != nil {
		return collectErr
	}
	return failure(ReasonCommandFailed, "journalctl exited non-zero")
}

// deniedAccess reads journalctl's own denial wording. The child runs under a
// fixed C locale precisely so this stays a stable string match; the text is
// matched and then discarded, never reported.
func deniedAccess(stderrText string) bool {
	lowered := strings.ToLower(stderrText)
	return strings.Contains(lowered, "permission denied") ||
		strings.Contains(lowered, "insufficient permissions")
}

// arguments builds the fixed argv. The unit rides as one attached
// --unit=<value> argument so a value opening with a dash can never become
// another option, and --all is never passed: it would disable journalctl's
// own oversized-field protection.
func arguments(unit string, entries int) []string {
	return []string{
		"--system",
		"--namespace=" + namespace,
		"--unit=" + unit,
		fmt.Sprintf("--lines=%d", entries),
		"--reverse",
		"--output=json",
		"--output-fields=" + outputFields,
		"--no-pager",
	}
}

// fixedEnvironment is the child's whole environment: deterministic enough for
// stderr matching, and inheriting no pager or shell behavior.
func fixedEnvironment() []string {
	return []string{"LC_ALL=C", "LANG=C", "SYSTEMD_COLORS=0"}
}
