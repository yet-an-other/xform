package xrayconfig

import (
	"slices"
	"testing"
)

// The roster store's adoption source (user-management spec §3–§4): every
// VLESS client in the config, as email → Client ID + inbound attachments.
// Non-VLESS inbounds feed the table labels only — the panel manages VLESS.
func TestParseCollectsVlessClientsForAdoption(t *testing.T) {
	roster, _, err := parse([]byte(`{
		"inbounds": [
			{"tag": "vless-vision", "protocol": "vless", "settings": {"clients": [
				{"email": "alice@example.com", "id": "alice-uuid"},
				{"email": "bob@example.com", "id": "bob-vision-uuid"},
				{"id": "no-identity-uuid"}
			]}},
			{"tag": "trojan-tls", "protocol": "trojan", "settings": {"clients": [
				{"email": "carol@example.com", "password": "secret"}
			]}},
			{"tag": "vless-xhttp", "protocol": "vless", "settings": {"clients": [
				{"email": "alice@example.com", "id": "alice-other-uuid"},
				{"email": "bob@example.com", "id": "bob-xhttp-uuid"}
			]}}
		]
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	clients := roster.Clients
	if len(clients) != 2 {
		t.Fatalf("clients = %v, want alice and bob alone — carol's inbound is not VLESS, the id-only client has no identity", clients)
	}
	alice := clients["alice@example.com"]
	if alice.ClientID != "alice-uuid" {
		t.Errorf("alice Client ID = %q, want the first inbound's alice-uuid", alice.ClientID)
	}
	if !slices.Equal(alice.Inbounds, []string{"vless-vision", "vless-xhttp"}) {
		t.Errorf("alice inbounds = %v, want both attachments in config order", alice.Inbounds)
	}
	bob := clients["bob@example.com"]
	if bob.ClientID != "bob-vision-uuid" || !slices.Equal(bob.Inbounds, []string{"vless-vision", "vless-xhttp"}) {
		t.Errorf("bob = %+v, want his first Client ID and both attachments", bob)
	}
	// Labels are untouched: carol still gets her table row.
	if _, ok := roster.Labels["carol@example.com"]; !ok {
		t.Error("labels lost carol — non-VLESS inbounds still feed the table")
	}
}

// A repeated email inside one inbound attaches the tag once. A client whose
// VLESS inbounds are all untagged is still adopted — with zero attachments,
// a profile-less user (user-management spec §3): the roster store keeps
// their Client ID even though no inbound can be named for them.
func TestParseCollectsEachAttachmentOnce(t *testing.T) {
	roster, _, err := parse([]byte(`{
		"inbounds": [
			{"tag": "vless-vision", "protocol": "vless", "settings": {"clients": [
				{"email": "alice@example.com", "id": "uuid-1"},
				{"email": "alice@example.com", "id": "uuid-2"}
			]}},
			{"protocol": "vless", "settings": {"clients": [{"email": "bob@example.com", "id": "uuid-3"}]}}
		]
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	alice := roster.Clients["alice@example.com"]
	if !slices.Equal(alice.Inbounds, []string{"vless-vision"}) {
		t.Errorf("alice inbounds = %v, want the tag once", alice.Inbounds)
	}
	bob, ok := roster.Clients["bob@example.com"]
	if !ok {
		t.Fatal("bob not adopted — an untagged inbound still defines a roster user")
	}
	if bob.ClientID != "uuid-3" || len(bob.Inbounds) != 0 {
		t.Errorf("bob = %+v, want his Client ID with zero attachments", bob)
	}
}
