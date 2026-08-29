package advertisements

import "testing"

// Reason mapping and the safe failure messages used to be observable only by
// driving a real file through fsnotify and a debounce. They are properties of
// the parse and of this source's own vocabulary, so they are tested as such.

const validDocument = `{
	"version": 1,
	"advertisements": [{
		"inbound_tag": "first",
		"topology": "direct",
		"host": "first.example.com",
		"port": 443,
		"transport": {"type": "tcp"},
		"security": {"type": "tls"}
	}]
}`

func TestParseSourcePublishesTheParsedView(t *testing.T) {
	view, reason, err := parseSource(View{}, []byte(validDocument))
	if err != nil {
		t.Fatalf("parseSource: %v (reason %q)", err, reason)
	}
	records := view.Advertisements()
	if len(records) != 1 || records[0].InboundTag != "first" {
		t.Errorf("advertisements = %+v, want the first record", records)
	}
}

// A schema the panel does not implement is its own reason: an admin who
// upgraded the file needs to be told that, not that it is malformed.
func TestParseSourceSeparatesAnUnsupportedVersionFromAMalformedFile(t *testing.T) {
	if _, reason, err := parseSource(View{}, []byte(`{"version":2,"advertisements":[]}`)); err == nil {
		t.Error("parseSource accepted an unsupported version")
	} else if reason != UnsupportedVersion {
		t.Errorf("reason = %q, want %q", reason, UnsupportedVersion)
	}

	if _, reason, err := parseSource(View{}, []byte(`{"version":1,`)); err == nil {
		t.Error("parseSource accepted a truncated document")
	} else if reason != ParseFailed {
		t.Errorf("reason = %q, want %q", reason, ParseFailed)
	}
}

// A failed parse publishes nothing: the watched source keeps the previous
// View, so this parse must not hand back a partial one.
func TestParseSourcePublishesNothingOnFailure(t *testing.T) {
	previous, _, err := parseSource(View{}, []byte(validDocument))
	if err != nil {
		t.Fatalf("seed parse: %v", err)
	}

	view, _, err := parseSource(previous, []byte(`{"version":1,"advertisements":[]} {"secret":"must-not-leak"}`))
	if err == nil {
		t.Fatal("parseSource accepted trailing content")
	}
	if len(view.Advertisements()) != 0 {
		t.Errorf("failed parse = %+v, want the zero View", view)
	}
}

// The messages a consumer sees name the file and what happens to profiles.
// They never carry the filesystem error or the document text.
func TestMessagesWordEachFailureForTheDashboard(t *testing.T) {
	const fallback = " Profiles use the last valid Advertised connection settings."
	for _, testCase := range []struct {
		reason ErrorReason
		fresh  string
	}{
		{ReadFailed, "The Advertised connection settings file could not be read."},
		{ParseFailed, "The Advertised connection settings file could not be parsed."},
		{UnsupportedVersion, "The Advertised connection settings version is not supported."},
	} {
		message := messages[testCase.reason]
		if message.Fresh != testCase.fresh {
			t.Errorf("%s fresh = %q, want %q", testCase.reason, message.Fresh, testCase.fresh)
		}
		if want := testCase.fresh + fallback; message.Stale != want {
			t.Errorf("%s stale = %q, want %q", testCase.reason, message.Stale, want)
		}
	}
}
