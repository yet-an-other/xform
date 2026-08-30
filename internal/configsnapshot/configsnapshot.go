// Package configsnapshot reads one exact, bounded Config snapshot from the
// configured xray path (SPEC §8).
//
// A Config snapshot is the exact UTF-8 text observed during one bounded read.
// It is separate from the parsed Roster in internal/xrayconfig: this package
// never parses, formats, or reflows what it reads, so a snapshot may show
// malformed JSON while the Roster keeps serving its last valid parse (SPEC §8).
package configsnapshot

import (
	"context"
	"fmt"
	"io"
	"time"
	"unicode/utf8"
)

// Reason is a stable read failure, safe to expose to the Dashboard (SPEC §8).
type Reason string

const (
	ReasonUnreadable Reason = "config_unreadable"
	ReasonTooLarge   Reason = "config_too_large"
	ReasonNotUTF8    Reason = "config_not_utf8"
)

// Error is a read failure carrying one stable reason.
//
// Detail is a short summary for the Panel's own logs, built from paths and
// bounds. It never carries file content: the snapshot exists to bound that
// content, and a diagnostic is not a place to leak it.
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

// Snapshot is the text observed during one bounded read.
type Snapshot struct {
	// CapturedAt is recorded only after every validation succeeds, so it
	// timestamps a snapshot that exists rather than a read that was attempted.
	CapturedAt time.Time
	// Path is the configured path string, never the resolved symlink target:
	// the admin asked about the file they configured (SPEC §8).
	Path string
	// SizeBytes is the number of bytes actually read.
	SizeBytes int64
	// Text is those bytes decoded as UTF-8 and nothing else: never parsed,
	// formatted, trimmed, or reflowed (OP-5).
	Text string
}

// maxBytes is the read bound SPEC §8 fixes: 4 MiB.
const maxBytes int64 = 4 << 20

// Reader reads Config snapshots from one configured path.
type Reader struct {
	path string

	// limit is maxBytes. It is a field only so tests can shrink it; nothing
	// outside this package can raise the bound the snapshot exists to enforce.
	limit int64

	// open reaches the filesystem; nil uses openPath. Overridden in tests.
	open openFile
	// now reads the wall clock; nil uses time.Now. Overridden in tests.
	now func() time.Time
}

// NewReader returns a Reader over the configured xray config path.
func NewReader(path string) *Reader {
	return &Reader{path: path, limit: maxBytes}
}

// Read collects one bounded snapshot. Nothing it reads is kept: the text
// belongs to the caller, and the Reader retains no copy (SPEC §8).
func (r *Reader) Read(ctx context.Context) (Snapshot, error) {
	// A caller who has gone away — a closed dialog aborts its request (SPEC §6) —
	// gets their own cancellation back rather than a read failure: no snapshot
	// was attempted, so no stable reason describes one.
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	open := r.open
	if open == nil {
		open = openPath
	}
	file, err := open(r.path)
	if err != nil {
		return Snapshot{}, failure(ReasonUnreadable, "cannot open the configured path: %s", err)
	}
	defer func() { _ = file.content.Close() }()

	if !file.regular {
		return Snapshot{}, failure(ReasonUnreadable, "the opened target is not a regular file")
	}

	// One byte past the cap, so a breach is detectable rather than silently
	// truncating the file into a snapshot that looks complete. Nothing is
	// trusted about the size the filesystem reports: the bound rides on the
	// read itself.
	bounded := io.LimitReader(cancelling{ctx: ctx, reader: file.content}, r.limit+1)
	content, err := io.ReadAll(bounded)
	if err != nil {
		if cancelled := ctx.Err(); cancelled != nil {
			return Snapshot{}, cancelled
		}
		return Snapshot{}, failure(ReasonUnreadable, "cannot read the configured file: %s", err)
	}
	if int64(len(content)) > r.limit {
		return Snapshot{}, failure(ReasonTooLarge, "the configured file exceeds %d bytes", r.limit)
	}
	// Text is a Go string either way, so the check is what keeps an invalid
	// encoding from becoming replacement characters the Dashboard would show
	// as if they were in the file.
	if !utf8.Valid(content) {
		return Snapshot{}, failure(ReasonNotUTF8, "the configured file is not valid UTF-8")
	}

	return Snapshot{
		CapturedAt: r.clock(),
		Path:       r.path,
		SizeBytes:  int64(len(content)),
		Text:       string(content),
	}, nil
}

// cancelling stops between chunks once the caller is gone, so a slow or hung
// filesystem cannot keep reading for a request that no longer exists.
type cancelling struct {
	ctx    context.Context
	reader io.Reader
}

func (c cancelling) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.reader.Read(p)
}

func (r *Reader) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}
