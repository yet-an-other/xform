package profiles_test

import (
	"fmt"
	"testing"

	"github.com/yet-an-other/xform/internal/profiles"
)

const (
	fixtureEmail = "alice@example.com"
	fixtureID    = "1d37a118-4f1b-4dc0-9e3c-3426b07518df"
	realityKey   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

func TestEvaluateSupportsApprovedTransportAndSecurityMatrix(t *testing.T) {
	tests := []struct {
		name                string
		inboundTransport    string
		inboundSecurity     string
		transportSettings   string
		securitySettings    string
		advertisedTransport string
		advertisedSecurity  string
		wantQuery           string
	}{
		{
			name: "TCP with TLS", inboundTransport: "raw", inboundSecurity: "tls",
			advertisedTransport: `{"type":"tcp"}`, advertisedSecurity: `{"type":"tls"}`,
			wantQuery: "type=tcp&encryption=none&security=tls&fp=chrome&sni=edge.example.com",
		},
		{
			name: "WebSocket with TLS", inboundTransport: "websocket", inboundSecurity: "tls",
			transportSettings:   `,"wsSettings":{"path":"/socket","host":"origin.example.com"}`,
			advertisedTransport: `{"type":"ws","path":"/socket","host":"origin.example.com"}`, advertisedSecurity: `{"type":"tls"}`,
			wantQuery: "type=ws&encryption=none&security=tls&path=%2Fsocket&host=origin.example.com&fp=chrome&sni=edge.example.com",
		},
		{
			name: "HTTPUpgrade with TLS", inboundTransport: "httpupgrade", inboundSecurity: "tls",
			transportSettings:   `,"httpupgradeSettings":{"path":"/upgrade","host":"origin.example.com"}`,
			advertisedTransport: `{"type":"httpupgrade","path":"/upgrade","host":"origin.example.com"}`, advertisedSecurity: `{"type":"tls"}`,
			wantQuery: "type=httpupgrade&encryption=none&security=tls&path=%2Fupgrade&host=origin.example.com&fp=chrome&sni=edge.example.com",
		},
		{
			name: "gRPC with TLS", inboundTransport: "grpc", inboundSecurity: "tls",
			transportSettings:   `,"grpcSettings":{"serviceName":"xform.Profile","authority":"grpc.example.com","multiMode":true}`,
			advertisedTransport: `{"type":"grpc","service_name":"xform.Profile","mode":"multi","authority":"grpc.example.com"}`, advertisedSecurity: `{"type":"tls"}`,
			wantQuery: "type=grpc&encryption=none&security=tls&serviceName=xform.Profile&mode=multi&authority=grpc.example.com&fp=chrome&sni=edge.example.com",
		},
		{
			name: "XHTTP with TLS", inboundTransport: "splithttp", inboundSecurity: "tls",
			transportSettings:   `,"splithttpSettings":{"path":"/split","host":"origin.example.com","mode":"stream-up"}`,
			advertisedTransport: `{"type":"xhttp","path":"/split","host":"origin.example.com","mode":"stream-up"}`, advertisedSecurity: `{"type":"tls"}`,
			wantQuery: "type=xhttp&encryption=none&security=tls&path=%2Fsplit&host=origin.example.com&mode=stream-up&fp=chrome&sni=edge.example.com",
		},
		{
			name: "TCP with REALITY", inboundTransport: "tcp", inboundSecurity: "reality",
			securitySettings:    `,"realitySettings":{"serverNames":["cover.example.com"],"shortIds":["0123"]}`,
			advertisedTransport: `{"type":"tcp"}`, advertisedSecurity: fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"0123"}`, realityKey),
			wantQuery: "type=tcp&encryption=none&security=reality&fp=chrome&sni=cover.example.com&pbk=" + realityKey + "&sid=0123",
		},
		{
			name: "gRPC with REALITY", inboundTransport: "grpc", inboundSecurity: "reality",
			transportSettings:   `,"grpcSettings":{"serviceName":"reality.Profile"}`,
			securitySettings:    `,"realitySettings":{"serverNames":["cover.example.com"],"shortIds":[""]}`,
			advertisedTransport: `{"type":"grpc","service_name":"reality.Profile"}`, advertisedSecurity: fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":""}`, realityKey),
			wantQuery: "type=grpc&encryption=none&security=reality&serviceName=reality.Profile&mode=gun&fp=chrome&sni=cover.example.com&pbk=" + realityKey + "&sid=",
		},
		{
			name: "XHTTP with REALITY", inboundTransport: "xhttp", inboundSecurity: "reality",
			transportSettings:   `,"xhttpSettings":{"path":"/x","host":"origin.example.com","mode":"auto"}`,
			securitySettings:    `,"realitySettings":{"serverNames":["cover.example.com"],"shortIds":["ABCD"]}`,
			advertisedTransport: `{"type":"xhttp","path":"/x","host":"origin.example.com","mode":"auto"}`, advertisedSecurity: fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"abcd"}`, realityKey),
			wantQuery: "type=xhttp&encryption=none&security=reality&path=%2Fx&host=origin.example.com&mode=auto&fp=chrome&sni=cover.example.com&pbk=" + realityKey + "&sid=abcd",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			xray := parseXray(t, fmt.Sprintf(`{"inbounds":[{
				"tag":"main","protocol":"vless",
				"settings":{"decryption":"none","clients":[{"email":%q,"id":%q}]},
				"streamSettings":{"network":%q,"security":%q%s%s}
			}]}`, fixtureEmail, fixtureID, test.inboundTransport, test.inboundSecurity, test.transportSettings, test.securitySettings))
			advertised := parseAdvertisements(t, fmt.Sprintf(`{"version":1,"advertisements":[{
				"inbound_tag":"main","name":"Main","topology":"direct","host":"edge.example.com","port":443,
				"transport":%s,"security":%s
			}]}`, test.advertisedTransport, test.advertisedSecurity))

			got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
				XrayView: xray, XrayAvailable: true,
				AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
			})
			if len(got.Items) != 1 || got.Items[0].Available == nil {
				t.Fatalf("result = %+v, want one available profile", got)
			}
			wantURI := "vless://" + fixtureID + "@edge.example.com:443?" + test.wantQuery + "#alice%40example.com%20%C2%B7%20Main"
			if got.Items[0].Available.URI != wantURI {
				t.Errorf("URI = %q, want %q", got.Items[0].Available.URI, wantURI)
			}
		})
	}
}
