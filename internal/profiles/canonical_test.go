package profiles_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/yet-an-other/xform/internal/profiles"
)

func TestEvaluateCanonicalizesAndEscapesTheCompleteURI(t *testing.T) {
	email := "a+b /ü@example.com"
	xray := parseXray(t, fmt.Sprintf(`{"inbounds":[{
		"tag":"main","protocol":"vless",
		"settings":{"decryption":"none","clients":[{"email":%q,"id":"alice-custom-id"}]},
		"streamSettings":{"network":"raw","security":"tls"}
	}]}`, email))
	advertised := parseAdvertisements(t, `{"version":1,"advertisements":[{
		"inbound_tag":"main","name":"N !'()*~","topology":"fronted","host":"BÜCHER.Example","port":443,
		"transport":{"type":"xhttp","path":"/a b+c&d","host":"Host + value","mode":"auto","extra":{"z":1e30,"a":"x/ü"}},
		"security":{"type":"tls","server_name":"SNI.BÜCHER.Example","alpn":["h2","http/1.1"],"ech":"a+b/c","certificate_pins":["pin one","pin/two"],"verify_name":"VERIFY.BÜCHER.Example"}
	}]}`)
	sources := profiles.Sources{
		XrayView: xray, XrayAvailable: true,
		AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
	}

	got := profiles.Evaluate(email, false, sources)
	if len(got.Items) != 1 || got.Items[0].Available == nil {
		t.Fatalf("result = %+v, want one available profile", got)
	}
	profile := got.Items[0].Available
	wantURI := "vless://05f19944-f3b9-5d9a-af19-6b9b42a844ed@xn--bcher-kva.example:443?" +
		"type=xhttp&encryption=none&security=tls&" +
		"path=%2Fa%20b%2Bc%26d&host=Host%20%2B%20value&mode=auto&" +
		"extra=%7B%22a%22%3A%22x%2F%C3%BC%22%2C%22z%22%3A1e%2B30%7D&" +
		"fp=chrome&sni=sni.xn--bcher-kva.example&alpn=h2%2Chttp%2F1.1&ech=a%2Bb%2Fc&" +
		"pcs=pin%20one%2Cpin%2Ftwo&vcn=verify.xn--bcher-kva.example#" +
		"a%2Bb%20%2F%C3%BC%40example.com%20%C2%B7%20N%20!'()*~"
	if profile.URI != wantURI {
		t.Errorf("URI = %q, want %q", profile.URI, wantURI)
	}
	if profile.Endpoint.Host != "xn--bcher-kva.example" || profile.ClientID != "05f19944-f3b9-5d9a-af19-6b9b42a844ed" {
		t.Errorf("canonical endpoint/Client ID = %+v / %q", profile.Endpoint, profile.ClientID)
	}
	if got, want := string(profile.Transport.Extra), `{"a":"x/ü","z":1e+30}`; got != want {
		t.Errorf("canonical XHTTP extra = %q, want %q", got, want)
	}
	if got, want := profile.Security.ALPN, []string{"h2", "http/1.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ALPN = %v, want %v", got, want)
	}
	if profile.Transport.Path != "/a b+c&d" || profile.Transport.Host != "Host + value" ||
		profile.Security.ServerName != "sni.xn--bcher-kva.example" || profile.Security.ECH != "a+b/c" ||
		profile.Security.VerifyName != "verify.xn--bcher-kva.example" {
		t.Errorf("typed public values are incomplete: transport %+v security %+v", profile.Transport, profile.Security)
	}

	for attempt := 0; attempt < 20; attempt++ {
		repeated := profiles.Evaluate(email, false, sources)
		if repeated.Items[0].Available == nil || repeated.Items[0].Available.URI != wantURI {
			t.Fatalf("attempt %d = %+v, want byte-for-byte repeatability", attempt, repeated)
		}
	}
}

func TestEvaluateCanonicalizesAdvertisedHostForms(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{host: "192.0.2.1", want: "192.0.2.1"},
		{host: "2001:0db8::1", want: "[2001:db8::1]"},
		{host: "[2001:0db8::1]", want: "[2001:db8::1]"},
		{host: "BÜCHER.example", want: "xn--bcher-kva.example"},
	}
	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "tls", "")))
			advertised := parseAdvertisements(t, fmt.Sprintf(`{"version":1,"advertisements":[{
				"inbound_tag":"main","topology":"fronted","host":%q,"port":443,
				"transport":{"type":"tcp"},"security":{"type":"tls","server_name":"sni.example.com"}
			}]}`, test.host))
			got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
				XrayView: xray, XrayAvailable: true,
				AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
			})
			if len(got.Items) != 1 || got.Items[0].Available == nil {
				t.Fatalf("result = %+v, want available profile", got)
			}
			if got.Items[0].Available.Endpoint.Host != test.want {
				t.Errorf("canonical host = %q, want %q", got.Items[0].Available.Endpoint.Host, test.want)
			}
		})
	}
}
