package journal

import (
	"context"
	"strings"
	"testing"
)

// collectRecord runs one raw journalctl object through the reader and returns
// the entry it normalized to. Normalization is exercised through collection
// because that is the only way it ever runs.
func collectRecord(t *testing.T, object string) (Entry, error) {
	t.Helper()
	reader, _ := newReader(t, processWith(object+"\n", nil))
	snapshot, err := collect(t, reader, SourcePanel)
	if err != nil {
		return Entry{}, err
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(snapshot.Entries))
	}
	return snapshot.Entries[0], nil
}

// mustCollectRecord fails the test if the record did not normalize.
func mustCollectRecord(t *testing.T, object string) Entry {
	t.Helper()
	entry, err := collectRecord(t, object)
	if err != nil {
		t.Fatalf("Collect() error = %v, want the record to normalize", err)
	}
	return entry
}

// valid wraps message JSON in an otherwise-complete record.
func valid(message string) string {
	return `{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"1723800000000000",` + message + `}`
}

func TestNormalizeMessageForms(t *testing.T) {
	tests := []struct {
		name          string
		field         string
		wantMessage   string
		wantEncoding  string
		wantNilText   bool
		wantTruncated bool
	}{
		{
			name:         "ordinary UTF-8 text passes through unchanged",
			field:        `"MESSAGE":"Panel started · ready"`,
			wantMessage:  "Panel started · ready",
			wantEncoding: "utf-8",
		},
		{
			name:         "a repeated field joins on newlines",
			field:        `"MESSAGE":["first line","second line"]`,
			wantMessage:  "first line\nsecond line",
			wantEncoding: "utf-8",
		},
		{
			name:         "binary data becomes base64",
			field:        `"MESSAGE":[104,105,255]`,
			wantMessage:  "aGn/",
			wantEncoding: "base64",
		},
		{
			name:         "a missing field is an empty message",
			field:        `"PRIORITY":"6"`,
			wantMessage:  "",
			wantEncoding: "utf-8",
		},
		{
			// journalctl elides a field past its size limit rather than
			// returning it; --all would disable that and is never passed.
			name:          "null marks journalctl's own elision",
			field:         `"MESSAGE":null`,
			wantNilText:   true,
			wantTruncated: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := mustCollectRecord(t, valid(test.field))

			if test.wantNilText {
				if entry.Message != nil || entry.MessageEncoding != nil {
					t.Errorf("message/encoding = %v/%v, want both nil", entry.Message, entry.MessageEncoding)
				}
			} else {
				if entry.Message == nil || *entry.Message != test.wantMessage {
					t.Errorf("Message = %v, want %q", entry.Message, test.wantMessage)
				}
				if entry.MessageEncoding == nil || *entry.MessageEncoding != test.wantEncoding {
					t.Errorf("MessageEncoding = %v, want %q", entry.MessageEncoding, test.wantEncoding)
				}
			}
			if entry.MessageTruncated != test.wantTruncated {
				t.Errorf("MessageTruncated = %v, want %v", entry.MessageTruncated, test.wantTruncated)
			}
		})
	}
}

func TestNormalizeDerivesTheUnitFromTrustedFieldsInOrder(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		want   string
	}{
		{
			name:   "the emitting unit wins",
			fields: `"_SYSTEMD_UNIT":"xform.service","UNIT":"other.service"`,
			want:   "xform.service",
		},
		{
			name:   "then the unit a PID 1 record is about",
			fields: `"UNIT":"about.service","OBJECT_SYSTEMD_UNIT":"object.service"`,
			want:   "about.service",
		},
		{
			name:   "then the object unit",
			fields: `"OBJECT_SYSTEMD_UNIT":"object.service","COREDUMP_UNIT":"dumped.service"`,
			want:   "object.service",
		},
		{
			name:   "then the coredump unit",
			fields: `"COREDUMP_UNIT":"dumped.service"`,
			want:   "dumped.service",
		},
		{
			// Nothing trusted to read: the snapshot's own fixed unit stands.
			name:   "otherwise the unit the snapshot was collected for",
			fields: `"MESSAGE":"no unit fields"`,
			want:   "xform.service",
		},
		{
			name:   "an empty trusted value is skipped",
			fields: `"_SYSTEMD_UNIT":"","UNIT":"about.service"`,
			want:   "about.service",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := mustCollectRecord(t, valid(test.fields))

			if entry.Unit != test.want {
				t.Errorf("Unit = %q, want %q", entry.Unit, test.want)
			}
		})
	}
}

func TestNormalizeClientFieldsFallBackToNull(t *testing.T) {
	tests := []struct {
		name           string
		fields         string
		wantIdentifier *string
		wantPID        *int
		wantPriority   *int
	}{
		{
			name:           "scalar values are taken",
			fields:         `"SYSLOG_IDENTIFIER":"xray","_PID":"1427","PRIORITY":"3"`,
			wantIdentifier: pointerTo("xray"),
			wantPID:        pointerTo(1427),
			wantPriority:   pointerTo(3),
		},
		{
			// These are untrusted client fields, so an odd shape normalizes
			// away instead of rejecting the whole snapshot.
			name:   "repeated values normalize to null",
			fields: `"SYSLOG_IDENTIFIER":["a","b"],"_PID":["1","2"],"PRIORITY":["6","7"]`,
		},
		{
			name:   "null values normalize to null",
			fields: `"SYSLOG_IDENTIFIER":null,"_PID":null,"PRIORITY":null`,
		},
		{
			name:   "non-numeric and out-of-range numbers normalize to null",
			fields: `"_PID":"not-a-pid","PRIORITY":"8"`,
		},
		{
			name:   "signed forms are not decimal journal values",
			fields: `"_PID":"-1","PRIORITY":"+6"`,
		},
		{
			name:   "absent fields are null",
			fields: `"MESSAGE":"nothing else"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := mustCollectRecord(t, valid(test.fields))

			assertStringPointer(t, "Identifier", entry.Identifier, test.wantIdentifier)
			assertIntPointer(t, "PID", entry.PID, test.wantPID)
			assertIntPointer(t, "Priority", entry.Priority, test.wantPriority)
		})
	}
}

func TestNormalizeRejectsTheWholeSnapshot(t *testing.T) {
	tests := []struct {
		name   string
		object string
	}{
		{
			name:   "no cursor",
			object: `{"__REALTIME_TIMESTAMP":"1723800000000000","MESSAGE":"m"}`,
		},
		{
			name:   "empty cursor",
			object: `{"__CURSOR":"","__REALTIME_TIMESTAMP":"1723800000000000"}`,
		},
		{
			name:   "cursor is not a scalar string",
			object: `{"__CURSOR":["a"],"__REALTIME_TIMESTAMP":"1723800000000000"}`,
		},
		{
			name:   "no timestamp",
			object: `{"__CURSOR":"c1","MESSAGE":"m"}`,
		},
		{
			name:   "timestamp is not decimal",
			object: `{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"-5"}`,
		},
		{
			name:   "timestamp overflows an unsigned 64-bit value",
			object: `{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"99999999999999999999999"}`,
		},
		{
			name:   "timestamp is a JSON number rather than journald's string",
			object: `{"__CURSOR":"c1","__REALTIME_TIMESTAMP":1723800000000000}`,
		},
		{
			// A trusted field cannot be forged, so an unexpected shape means
			// this is not the output the contract was written against.
			name:   "a trusted unit field is repeated",
			object: valid(`"_SYSTEMD_UNIT":["a.service","b.service"]`),
		},
		{
			name:   "a trusted unit field is a number",
			object: valid(`"_SYSTEMD_UNIT":42`),
		},
		{
			name:   "the message is an object",
			object: valid(`"MESSAGE":{"text":"m"}`),
		},
		{
			name:   "the message mixes strings and bytes",
			object: valid(`"MESSAGE":["text",104]`),
		},
		{
			name:   "the message carries a value outside a byte",
			object: valid(`"MESSAGE":[104,256]`),
		},
		{
			name:   "the message is an empty array",
			object: valid(`"MESSAGE":[]`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := collectRecord(t, test.object)

			if err == nil {
				t.Fatal("Collect() error = nil, want the snapshot rejected")
			}
			if got := reasonOf(t, err); got != ReasonMalformedOutput {
				t.Errorf("reason = %q, want %q", got, ReasonMalformedOutput)
			}
		})
	}
}

func TestNormalizeRejectsRatherThanSkippingOneBadRecord(t *testing.T) {
	// A snapshot missing entries it never mentioned would misrepresent the
	// journal, so one bad record fails the whole read.
	stdout := valid(`"MESSAGE":"good"`) + "\n" +
		`{"__CURSOR":"","__REALTIME_TIMESTAMP":"1723800000000000"}` + "\n"
	reader, _ := newReader(t, processWith(stdout, nil))

	snapshot, err := reader.Collect(context.Background(), SourcePanel)

	if err == nil {
		t.Fatal("Collect() error = nil, want the snapshot rejected")
	}
	if len(snapshot.Entries) != 0 {
		t.Errorf("entries = %d, want no partial snapshot", len(snapshot.Entries))
	}
	if !strings.Contains(err.Error(), "record 2") {
		t.Errorf("error %q does not say which record failed", err)
	}
}

func pointerTo[T any](value T) *T { return &value }

func assertStringPointer(t *testing.T, name string, got, want *string) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %q, want nil", name, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %q", name, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %q, want %q", name, *got, *want)
	}
}

func assertIntPointer(t *testing.T, name string, got, want *int) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %d, want nil", name, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %d", name, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %d, want %d", name, *got, *want)
	}
}
