package xrayconfig_test

import (
	"testing"

	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The add-user dialog's inbound options and the attach-time flow default both
// read the inbound View (user-management spec §4, §6).
func TestParseViewCapturesInboundPorts(t *testing.T) {
	view, err := xrayconfig.ParseView([]byte(`{
		"inbounds": [
			{"tag": "a", "protocol": "vless", "port": 443, "settings": {}},
			{"tag": "b", "protocol": "vless", "port": "1000-2000", "settings": {}},
			{"tag": "c", "protocol": "vless", "settings": {}}
		]
	}`))
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}
	inbounds := view.Inbounds()
	if inbounds[0].Port != "443" {
		t.Errorf("numeric port = %q, want 443", inbounds[0].Port)
	}
	if inbounds[1].Port != "1000-2000" {
		t.Errorf("range port = %q, want the string as written", inbounds[1].Port)
	}
	if inbounds[2].Port != "" {
		t.Errorf("absent port = %q, want empty", inbounds[2].Port)
	}
}

// The flow default for a newly attached client (user-management spec §4):
// copy the inbound's first existing client's flow; with no clients, fall back
// to xtls-rprx-vision on REALITY tcp/xhttp inbounds and to empty elsewhere.
func TestDefaultFlow(t *testing.T) {
	view, err := xrayconfig.ParseView([]byte(`{
		"inbounds": [
			{"tag": "has-clients", "protocol": "vless", "settings": {"clients": [
				{"email": "alice@example.com", "id": "a", "flow": "xtls-rprx-vision"},
				{"email": "bob@example.com", "id": "b", "flow": ""}
			]}, "streamSettings": {"network": "tcp", "security": "tls"}},
			{"tag": "first-empty", "protocol": "vless", "settings": {"clients": [
				{"email": "carol@example.com", "id": "c"}
			]}, "streamSettings": {"network": "tcp", "security": "reality"}},
			{"tag": "empty-reality-tcp", "protocol": "vless", "settings": {"clients": []},
			 "streamSettings": {"network": "tcp", "security": "reality"}},
			{"tag": "empty-reality-raw", "protocol": "vless", "settings": {"clients": []},
			 "streamSettings": {"network": "raw", "security": "reality"}},
			{"tag": "empty-reality-xhttp", "protocol": "vless", "settings": {"clients": []},
			 "streamSettings": {"network": "xhttp", "security": "reality"}},
			{"tag": "empty-reality-ws", "protocol": "vless", "settings": {"clients": []},
			 "streamSettings": {"network": "ws", "security": "reality"}},
			{"tag": "empty-tls-tcp", "protocol": "vless", "settings": {"clients": []},
			 "streamSettings": {"network": "tcp", "security": "tls"}}
		]
	}`))
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}
	flows := map[string]string{}
	for _, inbound := range view.Inbounds() {
		flows[inbound.Tag] = xrayconfig.DefaultFlow(inbound)
	}
	want := map[string]string{
		"has-clients":         "xtls-rprx-vision",
		"first-empty":         "", // the first client's empty flow is copied, not the fallback
		"empty-reality-tcp":   "xtls-rprx-vision",
		"empty-reality-raw":   "xtls-rprx-vision",
		"empty-reality-xhttp": "xtls-rprx-vision",
		"empty-reality-ws":    "",
		"empty-tls-tcp":       "",
	}
	for tag, wantFlow := range want {
		if flows[tag] != wantFlow {
			t.Errorf("DefaultFlow(%s) = %q, want %q", tag, flows[tag], wantFlow)
		}
	}
}
