package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestJournalHandlerPrefixesEachSlogSeverity(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewJournalHandler(&output))

	logger.Debug("debug")
	logger.Info("info")
	logger.Warn("warning")
	logger.Error("error")

	if got, want := output.String(), "<7>debug\n<6>info\n<4>warning\n<3>error\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestJournalHandlerKeepsStructuredAttributes(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewJournalHandler(&output)).With("component", "watcher").WithGroup("source")

	logger.Warn("cannot load xray config; keeping the last valid value",
		"path", "/usr/local/etc/xray/config.json",
		"error", "permission denied",
	)

	got := output.String()
	for _, want := range []string{
		"<4>cannot load xray config; keeping the last valid value",
		"component=watcher",
		"source.path=/usr/local/etc/xray/config.json",
		`source.error="permission denied"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output = %q, want it to contain %q", got, want)
		}
	}
}

func TestJournalHandlerWritesOneRecordPerCall(t *testing.T) {
	output := &shortWriteBuffer{}
	handler := NewJournalHandler(output)
	record := slog.NewRecord(time.Time{}, slog.LevelWarn, "warning", 0)

	if err := handler.Handle(context.Background(), record); err == nil {
		t.Fatal("Handle() error = nil after a short write")
	}
}

type shortWriteBuffer struct{}

func (*shortWriteBuffer) Write(document []byte) (int, error) {
	return len(document) - 1, errors.New("write failed")
}
