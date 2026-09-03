// Package logging configures panel logs for systemd's journal stream.
package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// JournalHandler writes one sd-daemon priority prefix at the start of every
// line. systemd strips the prefix and records it as PRIORITY; outside systemd
// the prefix remains visible rather than losing the severity.
type JournalHandler struct {
	output io.Writer
	mu     *sync.Mutex
	ops    []handlerOp
}

type handlerOp struct {
	group string
	attrs []slog.Attr
}

// NewJournalHandler returns a text handler for a systemd journal stream.
func NewJournalHandler(output io.Writer) *JournalHandler {
	return &JournalHandler{output: output, mu: &sync.Mutex{}}
}

func (h *JournalHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *JournalHandler) Handle(ctx context.Context, record slog.Record) error {
	var encoded bytes.Buffer
	formatter := slog.Handler(slog.NewTextHandler(&encoded, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey || attr.Key == slog.LevelKey {
				return slog.Attr{}
			}
			return attr
		},
	}))
	for _, op := range h.ops {
		if op.group != "" {
			formatter = formatter.WithGroup(op.group)
		} else {
			formatter = formatter.WithAttrs(op.attrs)
		}
	}
	attrsRecord := slog.NewRecord(time.Time{}, record.Level, "", record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		attrsRecord.AddAttrs(attr)
		return true
	})
	if err := formatter.Handle(ctx, attrsRecord); err != nil {
		return err
	}
	attrs := strings.TrimSuffix(strings.TrimPrefix(encoded.String(), `msg=""`), "\n")

	var line strings.Builder
	line.WriteByte('<')
	line.WriteString(strconv.Itoa(journalPriority(record.Level)))
	line.WriteByte('>')
	line.WriteString(record.Message)
	line.WriteString(attrs)
	line.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.output, line.String())
	return err
}

func (h *JournalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.ops = append(append([]handlerOp(nil), h.ops...), handlerOp{attrs: append([]slog.Attr(nil), attrs...)})
	return &clone
}

func (h *JournalHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.ops = append(append([]handlerOp(nil), h.ops...), handlerOp{group: name})
	return &clone
}

func journalPriority(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return 3
	case level >= slog.LevelWarn:
		return 4
	case level >= slog.LevelInfo:
		return 6
	default:
		return 7
	}
}
