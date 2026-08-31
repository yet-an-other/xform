package xrayconfig

import (
	"slices"
	"testing"
)

// The roster version and the safe failure messages used to be observable only
// by driving a real file through fsnotify and a debounce. They are properties
// of the parse, so they are tested as such: no filesystem, no timers.

const oneUserDocument = `{
	"inbounds": [
		{"tag": "before", "protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "id": "before-id"}]}, "streamSettings": {"security": "reality"}}
	]
}`

// oneUserDocumentProfileEdit changes only what the Connection profile view
// sees (a REALITY server name): the roster — labels and clients — is
// byte-for-byte the one in oneUserDocument.
const oneUserDocumentProfileEdit = `{
	"inbounds": [
		{"tag": "before", "protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "id": "before-id"}]}, "streamSettings": {"security": "reality", "realitySettings": {"serverNames": ["www.example.com"]}}}
	]
}`

// oneUserDocumentReattached keeps alice's labels but moves her Client ID and
// attachment: an adoption-relevant edit with no label change.
const oneUserDocumentReattached = `{
	"inbounds": [
		{"tag": "after", "protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "id": "after-id"}]}, "streamSettings": {"security": "reality"}}
	]
}`

const twoUserDocument = `{
	"inbounds": [
		{"tag": "before", "protocol": "vless", "settings": {"clients": [{"email": "alice@example.com"}, {"email": "bob@example.com"}]}, "streamSettings": {"security": "reality"}}
	]
}`

// Version 0 means no config has ever parsed, so the collector leaves every
// user's labels and gone flags alone. The first successful parse leaves it.
func TestParseSourceBumpsTheVersionOnTheFirstParse(t *testing.T) {
	parsed, reason, err := parseSource(Parsed{}, []byte(oneUserDocument))
	if err != nil {
		t.Fatalf("parseSource: %v (reason %q)", err, reason)
	}
	if parsed.Version != 1 {
		t.Errorf("first version = %d, want 1", parsed.Version)
	}
	if len(parsed.Roster.Labels) != 1 {
		t.Errorf("first roster = %v, want alice alone", parsed.Roster.Labels)
	}
}

// A profile-only edit changes the View but neither the labels nor the
// clients, and re-syncing every user over it would be work for nothing.
func TestParseSourceKeepsTheVersionOnAProfileOnlyEdit(t *testing.T) {
	previous, _, err := parseSource(Parsed{}, []byte(oneUserDocument))
	if err != nil {
		t.Fatalf("seed parse: %v", err)
	}

	next, _, err := parseSource(previous, []byte(oneUserDocumentProfileEdit))
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	if next.Version != previous.Version {
		t.Errorf("version = %d after a profile-only edit, want an unchanged %d", next.Version, previous.Version)
	}
	inbound := next.View.Inbounds()[0]
	if !slices.Equal(inbound.Security.Reality.ServerNames(), []string{"www.example.com"}) {
		t.Errorf("updated view = %+v, want the edited REALITY server names", inbound)
	}
}

// A Client ID or attachment edit moves no label, but the roster store's
// adoption source changed, so the version moves with it.
func TestParseSourceBumpsTheVersionWhenClientsMove(t *testing.T) {
	previous, _, err := parseSource(Parsed{}, []byte(oneUserDocument))
	if err != nil {
		t.Fatalf("seed parse: %v", err)
	}

	next, _, err := parseSource(previous, []byte(oneUserDocumentReattached))
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	if next.Version != previous.Version+1 {
		t.Errorf("version = %d after a client edit, want %d", next.Version, previous.Version+1)
	}
	alice := next.Roster.Clients["alice@example.com"]
	if alice.ClientID != "after-id" || !slices.Equal(alice.Inbounds, []string{"after"}) {
		t.Errorf("alice's client = %+v, want the after attachment and Client ID", alice)
	}
}

func TestParseSourceBumpsTheVersionWhenTheRosterMoves(t *testing.T) {
	previous, _, err := parseSource(Parsed{}, []byte(oneUserDocument))
	if err != nil {
		t.Fatalf("seed parse: %v", err)
	}

	next, _, err := parseSource(previous, []byte(twoUserDocument))
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	if next.Version != previous.Version+1 {
		t.Errorf("version = %d after a roster change, want %d", next.Version, previous.Version+1)
	}
	if _, ok := next.Roster.Labels["bob@example.com"]; !ok {
		t.Errorf("roster = %v, want bob@example.com present", next.Roster.Labels)
	}
}

// A broken write names parse_failed and publishes nothing: the watched source
// keeps the previous value, so this parse must not return a partial one.
func TestParseSourceNamesParseFailedAndPublishesNothing(t *testing.T) {
	previous, _, err := parseSource(Parsed{}, []byte(oneUserDocument))
	if err != nil {
		t.Fatalf("seed parse: %v", err)
	}

	parsed, reason, err := parseSource(previous, []byte(`{"inbounds": [`))
	if err == nil {
		t.Fatal("parseSource accepted a truncated config")
	}
	if reason != ParseFailed {
		t.Errorf("reason = %q, want %q", reason, ParseFailed)
	}
	if parsed.Version != 0 || parsed.Roster.Labels != nil {
		t.Errorf("failed parse = %+v, want the zero value", parsed)
	}
}

// A second complete JSON value after the first is a config the panel will not
// guess at.
func TestParseSourceRejectsTrailingContent(t *testing.T) {
	if _, reason, err := parseSource(Parsed{}, []byte(`{"inbounds": []} {"privateKey": "must-not-leak"}`)); err == nil {
		t.Error("parseSource accepted trailing content")
	} else if reason != ParseFailed {
		t.Errorf("reason = %q, want %q", reason, ParseFailed)
	}
}

// The messages a consumer sees name the file and what happens to profiles.
// They never carry the filesystem error or the config text.
func TestMessagesWordEachFailureForTheDashboard(t *testing.T) {
	for _, testCase := range []struct {
		reason ErrorReason
		stale  bool
		want   string
	}{
		{ReadFailed, false, "The configured xray file could not be read."},
		{ReadFailed, true, "The configured xray file could not be read; profiles use the last valid parse."},
		{ParseFailed, false, "The configured xray file could not be parsed."},
		{ParseFailed, true, "The configured xray file could not be parsed; profiles use the last valid parse."},
	} {
		message := messages[testCase.reason]
		got := message.Fresh
		if testCase.stale {
			got = message.Stale
		}
		if got != testCase.want {
			t.Errorf("%s (stale %t) = %q, want %q", testCase.reason, testCase.stale, got, testCase.want)
		}
	}
}
