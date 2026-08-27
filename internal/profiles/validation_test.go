package profiles_test

import (
	"fmt"
	"testing"

	"github.com/yet-an-other/xform/internal/profiles"
)

func TestEvaluateChecksDirectAdvertisementsAgainstInbound(t *testing.T) {
	reality := fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"abcd"}`, realityKey)
	tests := []struct {
		name                string
		transport           string
		security            string
		streamExtra         string
		advertisedTransport string
		advertisedSecurity  string
	}{
		{name: "transport type", transport: "raw", security: "tls", advertisedTransport: `{"type":"ws","path":"/","host":"origin.example.com"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "WebSocket path", transport: "ws", security: "tls", streamExtra: `,"wsSettings":{"path":"/expected","host":"origin.example.com"}`, advertisedTransport: `{"type":"ws","path":"/other","host":"origin.example.com"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "WebSocket host", transport: "ws", security: "tls", streamExtra: `,"wsSettings":{"path":"/ws","host":"expected.example.com"}`, advertisedTransport: `{"type":"ws","path":"/ws","host":"other.example.com"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "HTTPUpgrade path", transport: "httpupgrade", security: "tls", streamExtra: `,"httpupgradeSettings":{"path":"/expected","host":"origin.example.com"}`, advertisedTransport: `{"type":"httpupgrade","path":"/other","host":"origin.example.com"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "gRPC service", transport: "grpc", security: "tls", streamExtra: `,"grpcSettings":{"serviceName":"expected"}`, advertisedTransport: `{"type":"grpc","service_name":"other"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "XHTTP path", transport: "xhttp", security: "tls", streamExtra: `,"xhttpSettings":{"path":"/expected","host":"origin.example.com","mode":"auto"}`, advertisedTransport: `{"type":"xhttp","path":"/other","host":"origin.example.com","mode":"auto"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "XHTTP host", transport: "xhttp", security: "tls", streamExtra: `,"xhttpSettings":{"path":"/x","host":"expected.example.com","mode":"auto"}`, advertisedTransport: `{"type":"xhttp","path":"/x","host":"other.example.com","mode":"auto"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "XHTTP mode", transport: "xhttp", security: "tls", streamExtra: `,"xhttpSettings":{"path":"/x","host":"origin.example.com","mode":"stream-up"}`, advertisedTransport: `{"type":"xhttp","path":"/x","host":"origin.example.com","mode":"packet-up"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "security type", transport: "raw", security: "reality", streamExtra: `,"realitySettings":{"serverNames":["cover.example.com"],"shortIds":["abcd"]}`, advertisedTransport: `{"type":"tcp"}`, advertisedSecurity: `{"type":"tls"}`},
		{name: "REALITY server name", transport: "raw", security: "reality", streamExtra: `,"realitySettings":{"serverNames":["other.example.com"],"shortIds":["abcd"]}`, advertisedTransport: `{"type":"tcp"}`, advertisedSecurity: reality},
		{name: "REALITY short ID", transport: "raw", security: "reality", streamExtra: `,"realitySettings":{"serverNames":["cover.example.com"],"shortIds":["ffff"]}`, advertisedTransport: `{"type":"tcp"}`, advertisedSecurity: reality},
		{name: "TLS rejected SNI", transport: "raw", security: "tls", streamExtra: `,"tlsSettings":{"serverName":"expected.example.com","rejectUnknownSni":true}`, advertisedTransport: `{"type":"tcp"}`, advertisedSecurity: `{"type":"tls","server_name":"other.example.com"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", test.transport, test.security, test.streamExtra)))
			advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "direct", test.advertisedTransport, test.advertisedSecurity)))
			got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
				XrayView: xray, XrayAvailable: true,
				AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
			})
			if len(got.Items) != 1 || got.Items[0].Unavailable == nil || got.Items[0].Unavailable.Reason != profiles.ReasonInboundMismatch {
				t.Errorf("result = %+v, want inbound_mismatch", got)
			}
		})
	}
}

func TestEvaluateAcceptsDirectGRPCServiceSelections(t *testing.T) {
	for _, test := range []struct {
		name        string
		serviceName string
		mode        string
	}{
		{name: "stream selection", serviceName: "/prefix/Tun", mode: "gun"},
		{name: "multi selection", serviceName: "/prefix/TunMulti", mode: "multi"},
	} {
		t.Run(test.name, func(t *testing.T) {
			xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "grpc", "tls", `,"grpcSettings":{"serviceName":"/prefix/Tun|TunMulti"}`)))
			transport := fmt.Sprintf(`{"type":"grpc","service_name":%q,"mode":%q,"authority":"client.example.com"}`, test.serviceName, test.mode)
			advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "direct", transport, `{"type":"tls"}`)))
			got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
				XrayView: xray, XrayAvailable: true,
				AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
			})
			if len(got.Items) != 1 || got.Items[0].Available == nil {
				t.Errorf("result = %+v, want accepted gRPC service selection", got)
			}
		})
	}
}

func TestEvaluateFrontedAdvertisementDoesNotClaimRouteEquality(t *testing.T) {
	xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "hysteria", "future", "")))
	advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "fronted", `{"type":"ws","path":"/public","host":"front.example.com"}`, `{"type":"tls"}`)))
	got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
		XrayView: xray, XrayAvailable: true,
		AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
	})
	if len(got.Items) != 1 || got.Items[0].Available == nil {
		t.Fatalf("result = %+v, want available fronted profile", got)
	}
	if got.Items[0].Available.Transport.Type != "ws" || got.Items[0].Available.Security.Type != "tls" {
		t.Errorf("fronted client values = %+v / %+v", got.Items[0].Available.Transport, got.Items[0].Available.Security)
	}
}

func TestEvaluateAppliesEffectiveFlowAndVisionCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		inboundFlow   string
		userFlow      string
		transport     string
		security      string
		wantAvailable bool
	}{
		{name: "inbound Vision with TCP TLS", inboundFlow: "xtls-rprx-vision", transport: `{"type":"tcp"}`, security: `{"type":"tls"}`, wantAvailable: true},
		{name: "User Vision with TCP REALITY", userFlow: "xtls-rprx-vision", transport: `{"type":"tcp"}`, security: fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"abcd"}`, realityKey), wantAvailable: true},
		{name: "Vision rejects WebSocket TLS", inboundFlow: "xtls-rprx-vision", transport: `{"type":"ws","path":"/ws","host":"origin.example.com"}`, security: `{"type":"tls"}`},
		{name: "Vision rejects gRPC REALITY", userFlow: "xtls-rprx-vision", transport: `{"type":"grpc","service_name":"service"}`, security: fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"abcd"}`, realityKey)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			userExtra := ""
			if test.userFlow != "" {
				userExtra = `,"flow":"` + test.userFlow + `"`
			}
			xray := parseXray(t, fmt.Sprintf(`{"inbounds":[{
				"tag":"main","protocol":"vless","settings":{"flow":%q,"decryption":"none","clients":[{"email":%q,"id":%q%s}]},
				"streamSettings":{"network":"raw","security":"tls"}
			}]}`, test.inboundFlow, fixtureEmail, fixtureID, userExtra))
			advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "fronted", test.transport, test.security)))
			got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
				XrayView: xray, XrayAvailable: true,
				AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
			})
			if test.wantAvailable {
				if got.Items[0].Available == nil || got.Items[0].Available.Flow == nil || *got.Items[0].Available.Flow != "xtls-rprx-vision" {
					t.Errorf("result = %+v, want available Vision profile", got)
				}
				return
			}
			if got.Items[0].Unavailable == nil || got.Items[0].Unavailable.Reason != profiles.ReasonInboundMismatch {
				t.Errorf("result = %+v, want Vision inbound_mismatch", got)
			}
		})
	}
}

func TestEvaluateRejectsUnsupportedDirectTransportFeatures(t *testing.T) {
	for _, streamExtra := range []string{
		`,"finalmask":{"type":"unknown"}`,
		`,"rawSettings":{"header":{"type":"http"}}`,
	} {
		xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "tls", streamExtra)))
		advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "direct", `{"type":"tcp"}`, `{"type":"tls"}`)))
		got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
			XrayView: xray, XrayAvailable: true,
			AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
		})
		if got.Items[0].Unavailable == nil || got.Items[0].Unavailable.Reason != profiles.ReasonUnsupportedTransport {
			t.Errorf("result = %+v, want unsupported_transport", got)
		}
	}
}
