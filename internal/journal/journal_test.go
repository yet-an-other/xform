package journal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProcess stands in for a journalctl child: fixed streams, a fixed exit
// status, and a record of whether the reader killed and reaped it.
type fakeProcess struct {
	stdout io.Reader
	stderr io.Reader
	exit   error

	// blockUntilKilled makes Wait hang the way a live child would, so timeout
	// and cancellation can be driven without sleeping.
	blockUntilKilled bool

	mu     sync.Mutex
	killed bool
	waited bool
}

func (p *fakeProcess) Stdout() io.Reader { return p.stdout }
func (p *fakeProcess) Stderr() io.Reader { return p.stderr }

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killed = true
	return nil
}

func (p *fakeProcess) Wait() error {
	p.mu.Lock()
	waited := !p.blockUntilKilled
	p.waited = true
	p.mu.Unlock()
	if waited {
		return p.exit
	}
	for {
		p.mu.Lock()
		killed := p.killed
		p.mu.Unlock()
		if killed {
			return errors.New("signal: killed")
		}
		time.Sleep(time.Millisecond)
	}
}

func (p *fakeProcess) wasKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

func (p *fakeProcess) wasReaped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waited
}

// blockingReader never reaches EOF until the process is killed, standing in
// for a child that is still writing.
type blockingReader struct{ process *fakeProcess }

func (r blockingReader) Read(p []byte) (int, error) {
	for {
		if r.process.wasKilled() {
			return 0, io.EOF
		}
		time.Sleep(time.Millisecond)
	}
}

// newReader builds a Reader over a real (empty) executable file, so the
// startup-safety re-check passes and the fake process supplies the output.
func newReader(t *testing.T, process *fakeProcess) (*Reader, *[]childCommand) {
	t.Helper()
	reader := NewReader(executableFile(t), "xray.service")
	commands := []childCommand{}
	reader.start = func(_ context.Context, command childCommand) (child, error) {
		commands = append(commands, command)
		return process, nil
	}
	return reader, &commands
}

// executableFile writes a file the Panel user may execute.
func executableFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journalctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake journalctl: %v", err)
	}
	return path
}

func processWith(stdout string, exit error) *fakeProcess {
	return &fakeProcess{
		stdout: strings.NewReader(stdout),
		stderr: strings.NewReader(""),
		exit:   exit,
	}
}

// record builds one journalctl JSON object with the given extra fields.
func record(cursor, timestamp, message string, extra ...string) string {
	fields := []string{
		fmt.Sprintf("%q:%q", "__CURSOR", cursor),
		fmt.Sprintf("%q:%q", "__REALTIME_TIMESTAMP", timestamp),
		fmt.Sprintf("%q:%s", "MESSAGE", message),
	}
	fields = append(fields, extra...)
	return "{" + strings.Join(fields, ",") + "}\n"
}

func collect(t *testing.T, reader *Reader, source Source) (Snapshot, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return reader.Collect(ctx, source)
}

// reasonOf asserts the error carries a stable collection reason.
func reasonOf(t *testing.T, err error) Reason {
	t.Helper()
	var collectionError *Error
	if !errors.As(err, &collectionError) {
		t.Fatalf("error %v is not a *Error", err)
	}
	return collectionError.Reason
}

func TestCollectReturnsTheSnapshotNewestFirst(t *testing.T) {
	// journalctl runs with --reverse, so the order it prints is the order the
	// snapshot keeps.
	stdout := record("c2", "1723800002000000", `"second"`, `"_SYSTEMD_UNIT":"xform.service"`, `"SYSLOG_IDENTIFIER":"xform"`, `"_PID":"1427"`, `"PRIORITY":"6"`) +
		record("c1", "1723800001000000", `"first"`, `"_SYSTEMD_UNIT":"xform.service"`)
	captured := time.Unix(1_723_800_009, 0)
	reader, _ := newReader(t, processWith(stdout, nil))
	reader.now = func() time.Time { return captured }

	snapshot, err := collect(t, reader, SourcePanel)

	if err != nil {
		t.Fatalf("Collect() error = %v, want nil", err)
	}
	if snapshot.Source != SourcePanel || snapshot.Unit != "xform.service" {
		t.Errorf("snapshot source/unit = %q/%q, want panel/xform.service", snapshot.Source, snapshot.Unit)
	}
	if snapshot.Limit != 500 {
		t.Errorf("Limit = %d, want 500", snapshot.Limit)
	}
	// captured_at is stamped after the child exits and every record validates.
	if !snapshot.CapturedAt.Equal(captured) {
		t.Errorf("CapturedAt = %v, want %v", snapshot.CapturedAt, captured)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(snapshot.Entries))
	}
	first := snapshot.Entries[0]
	if first.Cursor != "c2" || first.TimestampUS != 1_723_800_002_000_000 {
		t.Errorf("first entry = %+v, want cursor c2 at 1723800002000000", first)
	}
	if first.Unit != "xform.service" {
		t.Errorf("Unit = %q, want xform.service", first.Unit)
	}
	if first.Identifier == nil || *first.Identifier != "xform" {
		t.Errorf("Identifier = %v, want xform", first.Identifier)
	}
	if first.PID == nil || *first.PID != 1427 {
		t.Errorf("PID = %v, want 1427", first.PID)
	}
	if first.Priority == nil || *first.Priority != 6 {
		t.Errorf("Priority = %v, want 6", first.Priority)
	}
	if first.Message == nil || *first.Message != "second" {
		t.Errorf("Message = %v, want second", first.Message)
	}
	if snapshot.Entries[1].Cursor != "c1" {
		t.Errorf("second entry cursor = %q, want c1", snapshot.Entries[1].Cursor)
	}
}

func TestCollectRunsTheFixedCommand(t *testing.T) {
	reader, commands := newReader(t, processWith("", nil))

	if _, err := collect(t, reader, SourceXray); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	if len(*commands) != 1 {
		t.Fatalf("started %d processes, want 1", len(*commands))
	}
	command := (*commands)[0]
	wantArgs := []string{
		"--system",
		"--namespace=xform",
		"--unit=xray.service",
		"--lines=500",
		"--reverse",
		"--output=json",
		"--output-fields=__CURSOR,__REALTIME_TIMESTAMP,_SYSTEMD_UNIT,UNIT,OBJECT_SYSTEMD_UNIT,COREDUMP_UNIT,SYSLOG_IDENTIFIER,_PID,PRIORITY,MESSAGE",
		"--no-pager",
	}
	if len(command.Args) != len(wantArgs) {
		t.Fatalf("args = %q, want %q", command.Args, wantArgs)
	}
	for index, want := range wantArgs {
		if command.Args[index] != want {
			t.Errorf("args[%d] = %q, want %q", index, command.Args[index], want)
		}
	}
	// The unit rides as one attached argument, so a leading dash could never
	// become another option.
	for _, arg := range command.Args {
		if arg == "--unit" || arg == "--all" {
			t.Errorf("args contain %q; the unit must be attached and --all never passed", arg)
		}
	}
	// A deterministic environment, nothing inherited.
	wantEnv := map[string]bool{"LC_ALL=C": true, "LANG=C": true, "SYSTEMD_COLORS=0": true}
	if len(command.Env) != len(wantEnv) {
		t.Fatalf("env = %q, want exactly %v", command.Env, wantEnv)
	}
	for _, entry := range command.Env {
		if !wantEnv[entry] {
			t.Errorf("env carries %q, which is not one of the fixed values", entry)
		}
	}
}

func TestCollectSelectsTheUnitFromTheSourceAlone(t *testing.T) {
	tests := []struct {
		source   Source
		wantUnit string
	}{
		{SourcePanel, "xform.service"},
		{SourceXray, "xray@edge.service"},
	}
	for _, test := range tests {
		t.Run(string(test.source), func(t *testing.T) {
			reader := NewReader(executableFile(t), "xray@edge.service")
			var command childCommand
			reader.start = func(_ context.Context, started childCommand) (child, error) {
				command = started
				return processWith("", nil), nil
			}

			snapshot, err := collect(t, reader, test.source)

			if err != nil {
				t.Fatalf("Collect() error = %v", err)
			}
			if snapshot.Unit != test.wantUnit {
				t.Errorf("Unit = %q, want %q", snapshot.Unit, test.wantUnit)
			}
			if want := "--unit=" + test.wantUnit; !slices.Contains(command.Args, want) {
				t.Errorf("args %q do not carry %q", command.Args, want)
			}
		})
	}
}

func TestCollectRejectsAnUnknownSource(t *testing.T) {
	reader, commands := newReader(t, processWith("", nil))

	// Nothing outside the two fixed sources may reach journalctl.
	if _, err := collect(t, reader, Source("../../etc")); err == nil {
		t.Fatal("Collect() with an unknown source = nil error, want a failure")
	}
	if len(*commands) != 0 {
		t.Errorf("started %d processes, want none", len(*commands))
	}
}

func TestCollectAcceptsAnEmptyJournal(t *testing.T) {
	reader, _ := newReader(t, processWith("", nil))

	snapshot, err := collect(t, reader, SourcePanel)

	// A clean exit with no records is an empty snapshot, not a failure — and
	// it proves nothing about the deployment's ACLs either way.
	if err != nil {
		t.Fatalf("Collect() error = %v, want nil", err)
	}
	if len(snapshot.Entries) != 0 {
		t.Errorf("entries = %d, want 0", len(snapshot.Entries))
	}
	if snapshot.Entries == nil {
		t.Error("Entries is nil, want an empty slice")
	}
}

func TestCollectRejectsMoreRecordsThanTheLimit(t *testing.T) {
	var stdout strings.Builder
	for index := range 4 {
		stdout.WriteString(record(fmt.Sprintf("c%d", index), "1723800000000000", `"m"`))
	}
	process := processWith(stdout.String(), nil)
	reader, _ := newReader(t, process)
	reader.limits.entries = 3

	_, err := collect(t, reader, SourcePanel)

	if got := reasonOf(t, err); got != ReasonMalformedOutput {
		t.Errorf("reason = %q, want %q", got, ReasonMalformedOutput)
	}
	if !process.wasKilled() || !process.wasReaped() {
		t.Errorf("child killed = %v, reaped = %v; want both", process.wasKilled(), process.wasReaped())
	}
}

func TestCollectRejectsOversizedOutput(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		limits func(*limits)
	}{
		{
			name:   "stdout past its cap",
			stdout: record("c1", "1723800000000000", `"`+strings.Repeat("m", 400)+`"`),
			limits: func(limits *limits) { limits.stdoutBytes = 64 },
		},
		{
			name:   "stderr past its cap",
			stdout: "",
			stderr: strings.Repeat("e", 400),
			limits: func(limits *limits) { limits.stderrBytes = 64 },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			process := &fakeProcess{
				stdout: strings.NewReader(test.stdout),
				stderr: strings.NewReader(test.stderr),
			}
			reader, _ := newReader(t, process)
			test.limits(&reader.limits)

			_, err := collect(t, reader, SourcePanel)

			if got := reasonOf(t, err); got != ReasonOutputTooLarge {
				t.Errorf("reason = %q, want %q", got, ReasonOutputTooLarge)
			}
			if !process.wasReaped() {
				t.Error("child was not reaped")
			}
		})
	}
}

func TestCollectClassifiesChildFailures(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		exit   error
		want   Reason
	}{
		{
			name:   "denial while opening journal data",
			stderr: "Failed to open files: Permission denied\n",
			exit:   errors.New("exit status 1"),
			want:   ReasonAccessDenied,
		},
		{
			name:   "denial reported as insufficient permissions",
			stderr: "No journal files were opened due to insufficient permissions.\n",
			exit:   errors.New("exit status 1"),
			want:   ReasonAccessDenied,
		},
		{
			name:   "any other non-zero exit",
			stderr: "journalctl: unrecognized option\n",
			exit:   errors.New("exit status 2"),
			want:   ReasonCommandFailed,
		},
		{
			name:   "invalid JSON",
			stdout: "{not json}\n",
			want:   ReasonMalformedOutput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			process := &fakeProcess{
				stdout: strings.NewReader(test.stdout),
				stderr: strings.NewReader(test.stderr),
				exit:   test.exit,
			}
			reader, _ := newReader(t, process)

			_, err := collect(t, reader, SourcePanel)

			if got := reasonOf(t, err); got != test.want {
				t.Errorf("reason = %q, want %q", got, test.want)
			}
			if !process.wasReaped() {
				t.Error("child was not reaped")
			}
		})
	}
}

func TestCollectKeepsJournalTextOutOfDiagnostics(t *testing.T) {
	const secret = "user alice@example.com connected from 203.0.113.10"
	process := &fakeProcess{
		stdout: strings.NewReader(record("c1", "not-a-timestamp", `"`+secret+`"`)),
		stderr: strings.NewReader("journalctl: " + secret + "\n"),
		exit:   errors.New("exit status 1"),
	}
	reader, _ := newReader(t, process)

	_, err := collect(t, reader, SourcePanel)

	if err == nil {
		t.Fatal("Collect() error = nil, want a failure")
	}
	// Neither journalctl's stderr nor a journal message may reach a diagnostic.
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "203.0.113.10") {
		t.Errorf("error %q carries journal text", err)
	}
}

func TestCollectTimesOutAndReapsTheChild(t *testing.T) {
	process := &fakeProcess{blockUntilKilled: true, stderr: strings.NewReader("")}
	process.stdout = blockingReader{process: process}
	reader, _ := newReader(t, process)
	// The contract's five-second bound, narrowed so the test does not wait it out.
	reader.limits.timeout = 20 * time.Millisecond

	_, err := reader.Collect(context.Background(), SourcePanel)

	if got := reasonOf(t, err); got != ReasonTimeout {
		t.Errorf("reason = %q, want %q", got, ReasonTimeout)
	}
	if !process.wasKilled() || !process.wasReaped() {
		t.Errorf("child killed = %v, reaped = %v; want both", process.wasKilled(), process.wasReaped())
	}
}

func TestCollectCancellationKillsAndReapsTheChild(t *testing.T) {
	process := &fakeProcess{blockUntilKilled: true, stderr: strings.NewReader("")}
	process.stdout = blockingReader{process: process}
	reader, _ := newReader(t, process)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := reader.Collect(ctx, SourcePanel)

	// A client disconnect is not a reportable collection failure; it is the
	// caller's own cancellation coming back.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if !process.wasKilled() || !process.wasReaped() {
		t.Errorf("child killed = %v, reaped = %v; want both", process.wasKilled(), process.wasReaped())
	}
}

func TestCollectHoldsOneProcessSlotGlobally(t *testing.T) {
	process := &fakeProcess{blockUntilKilled: true, stderr: strings.NewReader("")}
	process.stdout = blockingReader{process: process}
	reader, _ := newReader(t, process)
	reader.limits.timeout = 200 * time.Millisecond
	started := 0
	var mu sync.Mutex
	reader.start = func(context.Context, childCommand) (child, error) {
		mu.Lock()
		started++
		mu.Unlock()
		return process, nil
	}

	running := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(running)
		_, _ = reader.Collect(context.Background(), SourcePanel)
	}()
	<-running
	// Give the first collection time to claim the slot.
	time.Sleep(20 * time.Millisecond)

	_, err := collect(t, reader, SourceXray)

	if got := reasonOf(t, err); got != ReasonSnapshotInProgress {
		t.Errorf("reason = %q, want %q", got, ReasonSnapshotInProgress)
	}
	mu.Lock()
	startedCount := started
	mu.Unlock()
	if startedCount != 1 {
		t.Errorf("started %d processes, want 1 — the second request must not spawn one", startedCount)
	}
	<-done

	// The slot frees again once the first collection finishes.
	reader.start = func(context.Context, childCommand) (child, error) {
		return processWith("", nil), nil
	}
	if _, err := collect(t, reader, SourcePanel); err != nil {
		t.Errorf("Collect() after the slot freed = %v, want nil", err)
	}
}

func TestCollectReportsAnUnusableExecutable(t *testing.T) {
	t.Run("removed after startup", func(t *testing.T) {
		path := executableFile(t)
		reader := NewReader(path, "xray.service")
		reader.start = func(context.Context, childCommand) (child, error) {
			t.Error("Start called with an unusable executable")
			return nil, errors.New("unreachable")
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove executable: %v", err)
		}

		_, err := collect(t, reader, SourcePanel)

		if got := reasonOf(t, err); got != ReasonJournalctlUnavailable {
			t.Errorf("reason = %q, want %q", got, ReasonJournalctlUnavailable)
		}
	})

	t.Run("execute permission lost", func(t *testing.T) {
		path := executableFile(t)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		reader := NewReader(path, "xray.service")

		_, err := collect(t, reader, SourcePanel)

		if got := reasonOf(t, err); got != ReasonJournalctlUnavailable {
			t.Errorf("reason = %q, want %q", got, ReasonJournalctlUnavailable)
		}
	})

	t.Run("child cannot start", func(t *testing.T) {
		reader := NewReader(executableFile(t), "xray.service")
		reader.start = func(context.Context, childCommand) (child, error) {
			return nil, errors.New("fork/exec: resource temporarily unavailable")
		}

		_, err := collect(t, reader, SourcePanel)

		if got := reasonOf(t, err); got != ReasonJournalctlUnavailable {
			t.Errorf("reason = %q, want %q", got, ReasonJournalctlUnavailable)
		}
	})
}

func TestCollectFollowsTheReasonPrecedence(t *testing.T) {
	// Several conditions can hold at once; §6.4 fixes which one is reported.
	tests := []struct {
		name    string
		stdout  string
		stderr  string
		exit    error
		limits  func(*limits)
		blocked bool
		want    Reason
	}{
		{
			name:   "denial outranks the non-zero exit it caused",
			stderr: "Failed to open files: Permission denied\n",
			exit:   errors.New("exit status 1"),
			want:   ReasonAccessDenied,
		},
		{
			name:   "oversized stderr outranks a malformed record",
			stdout: "{not json}\n",
			stderr: strings.Repeat("e", 400),
			limits: func(limits *limits) { limits.stderrBytes = 64 },
			want:   ReasonOutputTooLarge,
		},
		{
			name:   "an oversized stdout outranks the malformed record it truncates",
			stdout: record("c1", "1723800000000000", `"`+strings.Repeat("m", 400)+`"`),
			limits: func(limits *limits) { limits.stdoutBytes = 64 },
			want:   ReasonOutputTooLarge,
		},
		{
			name:   "a malformed record outranks the exit status",
			stdout: "{not json}\n",
			exit:   errors.New("exit status 1"),
			want:   ReasonMalformedOutput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			process := &fakeProcess{
				stdout: strings.NewReader(test.stdout),
				stderr: strings.NewReader(test.stderr),
				exit:   test.exit,
			}
			reader, _ := newReader(t, process)
			if test.limits != nil {
				test.limits(&reader.limits)
			}

			_, err := collect(t, reader, SourcePanel)

			if got := reasonOf(t, err); got != test.want {
				t.Errorf("reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCollectTimeoutOutranksTheChildsExitStatus(t *testing.T) {
	process := &fakeProcess{blockUntilKilled: true, stderr: strings.NewReader("")}
	process.stdout = blockingReader{process: process}
	reader, _ := newReader(t, process)
	reader.limits.timeout = 20 * time.Millisecond

	// The kill makes the child exit non-zero; the deadline is still the
	// reason the collection failed.
	_, err := reader.Collect(context.Background(), SourcePanel)

	if got := reasonOf(t, err); got != ReasonTimeout {
		t.Errorf("reason = %q, want %q", got, ReasonTimeout)
	}
}

// floodingReader keeps writing until the process is killed, standing in for a
// child filling a pipe nobody is draining.
type floodingReader struct{ process *fakeProcess }

func (r floodingReader) Read(p []byte) (int, error) {
	if r.process.wasKilled() {
		return 0, io.EOF
	}
	for index := range p {
		p[index] = 'e'
	}
	return len(p), nil
}

func TestCollectEndsAChildFloodingItsStderr(t *testing.T) {
	// The cap alone is not enough: nothing drains the rest of the pipe, so a
	// live child would block on it until the deadline and the breach would be
	// reported as a timeout instead.
	process := &fakeProcess{blockUntilKilled: true, stdout: strings.NewReader("")}
	process.stderr = floodingReader{process: process}
	reader, _ := newReader(t, process)
	reader.limits.stderrBytes = 64
	// Long enough that a deadline could not be what ends this.
	reader.limits.timeout = 5 * time.Second

	_, err := reader.Collect(context.Background(), SourcePanel)

	if got := reasonOf(t, err); got != ReasonOutputTooLarge {
		t.Errorf("reason = %q, want %q", got, ReasonOutputTooLarge)
	}
	if !process.wasKilled() || !process.wasReaped() {
		t.Errorf("child killed = %v, reaped = %v; want both", process.wasKilled(), process.wasReaped())
	}
}

func TestCollectKeepsASnapshotThatMerelyWarnedAboutDenial(t *testing.T) {
	// journalctl can warn about one file it could not open and still return a
	// complete snapshot; access_denied describes a child that failed.
	process := &fakeProcess{
		stdout: strings.NewReader(record("c1", "1723800000000000", `"still collected"`)),
		stderr: strings.NewReader("Failed to open files: Permission denied\n"),
	}
	reader, _ := newReader(t, process)

	snapshot, err := collect(t, reader, SourcePanel)

	if err != nil {
		t.Fatalf("Collect() error = %v, want the snapshot kept", err)
	}
	if len(snapshot.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(snapshot.Entries))
	}
}

func TestCollectRejectsAnUnknownSourceWithoutAStableReason(t *testing.T) {
	reader, commands := newReader(t, processWith("", nil))

	_, err := reader.Collect(context.Background(), Source("../../etc"))

	if err == nil {
		t.Fatal("Collect() with an unknown source = nil error, want a failure")
	}
	// Every §6.4 reason describes an attempt that reached journalctl; this one
	// never does, so it deliberately carries none.
	var collectionError *Error
	if errors.As(err, &collectionError) {
		t.Errorf("error carries reason %q, want a plain programming error", collectionError.Reason)
	}
	if len(*commands) != 0 {
		t.Errorf("started %d processes, want none", len(*commands))
	}
}
