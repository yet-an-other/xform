package configsnapshot

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// capturedAt is the fixed clock reading every test asserts against, so a
// Snapshot's timestamp proves the reader consulted the clock seam.
var capturedAt = time.Unix(1723800000, 0).UTC()

// newReader builds a Reader over a real path with the clock pinned.
func newReader(t *testing.T, path string) *Reader {
	t.Helper()
	reader := NewReader(path)
	reader.now = func() time.Time { return capturedAt }
	return reader
}

// writeFile puts content at a fresh path and returns it.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// requireReason fails unless err is a snapshot failure carrying want.
func requireReason(t *testing.T, err error, want Reason) {
	t.Helper()
	var snapshotErr *Error
	if !errors.As(err, &snapshotErr) {
		t.Fatalf("Read() error = %v, want a *Error with reason %s", err, want)
	}
	if snapshotErr.Reason != want {
		t.Errorf("Read() reason = %s, want %s", snapshotErr.Reason, want)
	}
}

func TestReadReturnsTheExactTextOfARegularFile(t *testing.T) {
	const content = "{\n  \"inbounds\": []\n}\n"
	path := writeFile(t, "config.json", content)

	snapshot, err := newReader(t, path).Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}

	if snapshot.Text != content {
		t.Errorf("Text = %q, want %q", snapshot.Text, content)
	}
	if snapshot.Path != path {
		t.Errorf("Path = %q, want the configured path %q", snapshot.Path, path)
	}
	if snapshot.SizeBytes != int64(len(content)) {
		t.Errorf("SizeBytes = %d, want %d", snapshot.SizeBytes, len(content))
	}
	if !snapshot.CapturedAt.Equal(capturedAt) {
		t.Errorf("CapturedAt = %v, want the clock reading %v", snapshot.CapturedAt, capturedAt)
	}
}

func TestReadFollowsARootConfiguredSymlinkAndReportsTheConfiguredPath(t *testing.T) {
	// The deployment may point XFORM_XRAY_CONFIG at a symlink; the admin asked
	// about that path, so the snapshot names it rather than its target (SPEC §8).
	const content = "{\"inbounds\":[]}\n"
	realFile := writeFile(t, "config.real.json", content)
	link := filepath.Join(filepath.Dir(realFile), "config.json")
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	snapshot, err := newReader(t, link).Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}

	if snapshot.Text != content {
		t.Errorf("Text = %q, want %q", snapshot.Text, content)
	}
	if snapshot.Path != link {
		t.Errorf("Path = %q, want the configured symlink path %q", snapshot.Path, link)
	}
	if snapshot.SizeBytes != int64(len(content)) {
		t.Errorf("SizeBytes = %d, want %d", snapshot.SizeBytes, len(content))
	}
}

func TestReadRejectsATargetThatIsNotARegularFile(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(t.TempDir(), "config.json")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "a directory", path: directory},
		{name: "a symlink to a directory", path: link},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newReader(t, test.path).Read(context.Background())
			requireReason(t, err, ReasonUnreadable)
		})
	}
}

func TestReadAcceptsAFileAtExactlyTheLimit(t *testing.T) {
	path := writeFile(t, "config.json", strings.Repeat("a", int(maxBytes)))

	snapshot, err := newReader(t, path).Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if snapshot.SizeBytes != maxBytes {
		t.Errorf("SizeBytes = %d, want the whole %d bytes", snapshot.SizeBytes, maxBytes)
	}
	if int64(len(snapshot.Text)) != maxBytes {
		t.Errorf("len(Text) = %d, want %d", len(snapshot.Text), maxBytes)
	}
}

func TestReadRejectsAFileOverTheLimit(t *testing.T) {
	path := writeFile(t, "config.json", strings.Repeat("a", int(maxBytes)+1))

	_, err := newReader(t, path).Read(context.Background())
	requireReason(t, err, ReasonTooLarge)
}

func TestReadStopsOneByteAfterTheLimit(t *testing.T) {
	// A file that never ends must cost the Panel one detection byte, not its
	// memory: the cap rides on the read itself, not on a size reported first.
	endless := &endlessReader{}
	reader := newReader(t, "/configured/config.json")
	reader.limit = 1024
	reader.open = func(string) (target, error) {
		return target{content: io.NopCloser(endless), regular: true}, nil
	}

	_, err := reader.Read(context.Background())

	requireReason(t, err, ReasonTooLarge)
	if endless.count > reader.limit+1 {
		t.Errorf("read %d bytes, want at most %d", endless.count, reader.limit+1)
	}
}

// endlessReader is a file that never reaches EOF, counting what was taken.
type endlessReader struct{ count int64 }

func (r *endlessReader) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = 'a'
	}
	r.count += int64(len(p))
	return len(p), nil
}

func TestReadRejectsInvalidUTF8(t *testing.T) {
	path := writeFile(t, "config.json", "{\"tag\":\"\xff\xfe\"}\n")

	_, err := newReader(t, path).Read(context.Background())

	requireReason(t, err, ReasonNotUTF8)
}

func TestReadConsultsTheClockOnlyAfterEveryValidationSucceeds(t *testing.T) {
	// captured_at timestamps a snapshot, so a read that never produced one
	// must not reach the clock at all (SPEC §8).
	tests := []struct {
		name  string
		path  string
		limit int64
	}{
		{name: "missing", path: filepath.Join(t.TempDir(), "absent.json")},
		{name: "not a regular file", path: t.TempDir()},
		{name: "over the limit", path: writeFile(t, "big.json", "{\"inbounds\":[]}\n"), limit: 4},
		{name: "invalid UTF-8", path: writeFile(t, "binary.json", "{\"tag\":\"\xff\xfe\"}\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := NewReader(test.path)
			if test.limit != 0 {
				reader.limit = test.limit
			}
			reader.now = func() time.Time {
				t.Error("the clock was consulted for a failed read")
				return time.Time{}
			}

			if _, err := reader.Read(context.Background()); err == nil {
				t.Fatal("Read() error = nil, want a rejection")
			}
		})
	}
}

func TestReadRejectsAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file regardless of its mode")
	}
	path := writeFile(t, "config.json", "{}\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	_, err := newReader(t, path).Read(context.Background())

	requireReason(t, err, ReasonUnreadable)
}

func TestReadRejectsAMissingFile(t *testing.T) {
	_, err := newReader(t, filepath.Join(t.TempDir(), "absent.json")).Read(context.Background())

	requireReason(t, err, ReasonUnreadable)
}

func TestReadRejectsAFailureMidStream(t *testing.T) {
	// A file that opens and then fails — a disappearing mount, an I/O error —
	// yields no snapshot: half a config is not the config.
	reader := newReader(t, "/configured/config.json")
	reader.open = func(string) (target, error) {
		return target{content: io.NopCloser(&failingReader{}), regular: true}, nil
	}

	_, err := reader.Read(context.Background())

	requireReason(t, err, ReasonUnreadable)
}

// failingReader hands over one chunk and then fails, like a file whose
// storage went away mid-read.
type failingReader struct{ served bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.served {
		return 0, errors.New("input/output error")
	}
	r.served = true
	return copy(p, "{\"inbounds\""), nil
}

func TestReadDoesNotLeakFileContentIntoTheFailureDetail(t *testing.T) {
	// The Detail rides into the Panel's own logs; the snapshot exists to bound
	// this content, and a diagnostic is not a place to leak it.
	const secret = "SUPER-SECRET-CLIENT-ID"
	path := writeFile(t, "config.json", secret)
	reader := newReader(t, path)
	reader.limit = int64(len(secret)) - 1

	_, err := reader.Read(context.Background())

	requireReason(t, err, ReasonTooLarge)
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Read() error = %q, want no file content in the detail", err)
	}
}

func TestReadPreservesReadableFilesByteForByte(t *testing.T) {
	// The snapshot is the exact text, not a parse: malformed JSON succeeds,
	// and nothing is trimmed, reflowed, or re-encoded (SPEC §8).
	tests := []struct {
		name    string
		content string
	}{
		{name: "malformed JSON with a final newline", content: "{\n  \"inbounds\": [\n}\n"},
		{name: "malformed JSON without a final newline", content: "{\"inbounds\": ["},
		{name: "several final newlines", content: "{}\n\n\n"},
		{name: "CRLF line endings", content: "{\r\n  \"inbounds\": []\r\n}\r\n"},
		{name: "leading and trailing whitespace", content: "  \n\t{}\n \t\n"},
		{name: "empty", content: ""},
		{name: "not JSON at all", content: "this is not a config\n"},
		{name: "multi-byte UTF-8", content: "{\"remark\":\"привет 🌍 café\"}\n"},
		{name: "a NUL byte", content: "{\"tag\":\"a\x00b\"}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeFile(t, "config.json", test.content)

			snapshot, err := newReader(t, path).Read(context.Background())
			if err != nil {
				t.Fatalf("Read() error = %v, want nil", err)
			}

			if snapshot.Text != test.content {
				t.Errorf("Text = %q, want the source bytes %q", snapshot.Text, test.content)
			}
			if snapshot.SizeBytes != int64(len(test.content)) {
				t.Errorf("SizeBytes = %d, want the %d bytes read", snapshot.SizeBytes, len(test.content))
			}
		})
	}
}

func TestReadObservesTheFileAsItIsNow(t *testing.T) {
	// Nothing is retained between reads: the Reader holds no copy of a
	// snapshot, so a second read sees the file that exists now (SPEC §8).
	path := writeFile(t, "config.json", "{\"first\":true}\n")
	reader := newReader(t, path)
	if _, err := reader.Read(context.Background()); err != nil {
		t.Fatalf("first Read() error = %v, want nil", err)
	}

	const replaced = "{\"second\":true}\n"
	if err := os.WriteFile(path, []byte(replaced), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	snapshot, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("second Read() error = %v, want nil", err)
	}
	if snapshot.Text != replaced {
		t.Errorf("Text = %q, want the current file %q", snapshot.Text, replaced)
	}
}

func TestReadDoesNotTouchTheFilesystemForACancelledCaller(t *testing.T) {
	// Closing the dialog aborts its request (SPEC §6); a caller already gone gets
	// their own cancellation back, and the file is never opened.
	reader := newReader(t, "/configured/config.json")
	reader.open = func(string) (target, error) {
		t.Error("the configured path was opened for a cancelled caller")
		return target{}, errors.New("unreachable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reader.Read(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Read() error = %v, want context.Canceled", err)
	}
}

func TestReadRejectsANamedPipeWithoutWaitingForAWriter(t *testing.T) {
	// A FIFO opened for reading parks until a writer appears. The Panel must
	// reject it as the non-regular file it is, not hang the request on it.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}

	failed := make(chan error, 1)
	go func() {
		_, err := newReader(t, path).Read(context.Background())
		failed <- err
	}()

	select {
	case err := <-failed:
		requireReason(t, err, ReasonUnreadable)
	case <-time.After(10 * time.Second):
		t.Fatal("Read() is still waiting on the named pipe, want a rejection")
	}
}

func TestReadRejectsACharacterDevice(t *testing.T) {
	const device = "/dev/zero"
	if _, err := os.Stat(device); err != nil {
		t.Skipf("stat %s: %v", device, err)
	}

	_, err := newReader(t, device).Read(context.Background())

	requireReason(t, err, ReasonUnreadable)
}

func TestReadRejectsAPathItMayNotOpen(t *testing.T) {
	// The permission case through the seam, so it is covered whatever user the
	// suite runs as — root reads every file regardless of its mode.
	reader := newReader(t, "/configured/config.json")
	reader.open = func(string) (target, error) {
		return target{}, fs.ErrPermission
	}

	_, err := reader.Read(context.Background())

	requireReason(t, err, ReasonUnreadable)
}

func TestReadStopsWhenTheCallerGoesAwayMidRead(t *testing.T) {
	// A hung or slow filesystem must not keep reading for a request that no
	// longer exists, and the caller gets their cancellation, not a reason.
	ctx, cancel := context.WithCancel(context.Background())
	endless := &endlessReader{}
	reader := newReader(t, "/configured/config.json")
	reader.open = func(string) (target, error) {
		return target{content: io.NopCloser(&cancelMidRead{cancel: cancel, reader: endless}), regular: true}, nil
	}

	_, err := reader.Read(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Read() error = %v, want context.Canceled", err)
	}
}

// cancelMidRead cancels the caller after serving its first chunk.
type cancelMidRead struct {
	cancel context.CancelFunc
	reader io.Reader
	served bool
}

func (r *cancelMidRead) Read(p []byte) (int, error) {
	if r.served {
		r.cancel()
	}
	r.served = true
	return r.reader.Read(p)
}
