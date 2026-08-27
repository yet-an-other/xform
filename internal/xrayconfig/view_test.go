package xrayconfig_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/xrayconfig"
)

func TestParseViewRetainsOrderedProfileCandidates(t *testing.T) {
	document, err := os.ReadFile("testdata/profile-candidates.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	view, err := xrayconfig.ParseView(document)
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}
	inbounds := view.Inbounds()
	if got, want := len(inbounds), 5; got != want {
		t.Fatalf("inbounds = %d, want %d", got, want)
	}

	if got, want := []string{
		inbounds[0].Transport.Type,
		inbounds[1].Transport.Type,
		inbounds[2].Transport.Type,
		inbounds[3].Transport.Type,
		inbounds[4].Transport.Type,
	}, []string{"raw", "ws", "httpupgrade", "grpc", "xhttp"}; !reflect.DeepEqual(got, want) {
		t.Errorf("transport order = %v, want %v", got, want)
	}

	reality := inbounds[0]
	if reality.Tag != "duplicate-tag" || reality.Protocol != "vless" ||
		reality.Flow != "xtls-rprx-vision" || reality.Decryption != "none" {
		t.Errorf("first inbound identity/settings = %+v", reality)
	}
	if !reality.Transport.RawHeaderConfigured || reality.Transport.RawHeaderType != "none" {
		t.Errorf("RAW settings = %+v, want configured none header", reality.Transport)
	}
	if got, want := reality.Security.Reality.ServerNames(), []string{"one.example.com", "two.example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("REALITY server names = %v, want %v", got, want)
	}
	if got, want := reality.Security.Reality.ShortIDs(), []string{"", "0123456789abcdef"}; !reflect.DeepEqual(got, want) {
		t.Errorf("REALITY short IDs = %v, want %v", got, want)
	}
	users := reality.Users()
	if got, want := len(users), 3; got != want {
		t.Fatalf("first inbound users = %d, want %d", got, want)
	}
	if users[0].Email != "alice@example.com" || users[0].ClientID != "alice-custom-id" ||
		users[0].Flow != "xtls-rprx-vision" || users[0].Reverse {
		t.Errorf("first User = %+v", users[0])
	}
	if users[1].Email != "alice@example.com" || users[1].ClientID != "alice-second-id" {
		t.Errorf("duplicate User = %+v", users[1])
	}
	if !users[2].Reverse || users[2].ClientID != "reverse-custom-id" {
		t.Errorf("reverse User = %+v", users[2])
	}

	websocket := inbounds[1]
	if websocket.Tag != "duplicate-tag" || websocket.Transport.WebSocket.Path != "/socket" ||
		websocket.Transport.WebSocket.Host != "ws.example.com" {
		t.Errorf("WebSocket inbound = %+v", websocket)
	}
	if got, want := websocket.Security.TLS.ALPN(), []string{"h2", "http/1.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("TLS ALPN = %v, want %v", got, want)
	}
	if websocket.Security.TLS.ServerName != "tls.example.com" || !websocket.Security.TLS.RejectUnknownSNI {
		t.Errorf("TLS settings = %+v", websocket.Security.TLS)
	}
	if got := websocket.Users(); len(got) != 1 || got[0].Email != "alice@example.com" || got[0].ClientID != "alice-websocket-id" {
		t.Errorf("WebSocket Users = %+v", got)
	}

	upgrade := inbounds[2]
	if upgrade.Tag != "" || upgrade.Transport.HTTPUpgrade.Path != "/upgrade" ||
		upgrade.Transport.HTTPUpgrade.Host != "upgrade.example.com" {
		t.Errorf("HTTPUpgrade inbound = %+v", upgrade)
	}

	grpc := inbounds[3]
	if grpc.Transport.GRPC.ServiceName != "xform.Profile" || grpc.Transport.GRPC.Authority != "grpc.example.com" ||
		!grpc.Transport.GRPC.MultiMode || grpc.Flow != "xtls-rprx-vision" {
		t.Errorf("gRPC inbound = %+v", grpc)
	}

	xhttp := inbounds[4]
	if xhttp.Transport.XHTTP.Path != "/split" || xhttp.Transport.XHTTP.Host != "xhttp.example.com" ||
		xhttp.Transport.XHTTP.Mode != "stream-up" || !xhttp.Transport.XHTTP.ExtraConfigured {
		t.Errorf("XHTTP inbound = %+v", xhttp)
	}

	formatted := fmt.Sprintf("%#v", inbounds)
	for _, secret := range []string{"must-not-be-retained", "must-not-be-retained-either", "must-not-be-retained-tls-key"} {
		if strings.Contains(formatted, secret) {
			t.Errorf("parsed view retained private server material %q", secret)
		}
	}
}

func TestParsedViewCannotBeMutatedThroughAccessors(t *testing.T) {
	document, err := os.ReadFile("testdata/profile-candidates.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	view, err := xrayconfig.ParseView(document)
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}

	inbounds := view.Inbounds()
	inbounds[0].Tag = "changed"
	users := inbounds[0].Users()
	users[0].ClientID = "changed"
	names := inbounds[0].Security.Reality.ServerNames()
	names[0] = "changed"
	alpn := inbounds[1].Security.TLS.ALPN()
	alpn[0] = "changed"

	fresh := view.Inbounds()
	if fresh[0].Tag != "duplicate-tag" {
		t.Errorf("inbound tag = %q after caller mutation, want duplicate-tag", fresh[0].Tag)
	}
	if got := fresh[0].Users()[0].ClientID; got != "alice-custom-id" {
		t.Errorf("Client ID = %q after caller mutation, want alice-custom-id", got)
	}
	if got := fresh[0].Security.Reality.ServerNames()[0]; got != "one.example.com" {
		t.Errorf("REALITY server name = %q after caller mutation, want one.example.com", got)
	}
	if got := fresh[1].Security.TLS.ALPN()[0]; got != "h2" {
		t.Errorf("TLS ALPN = %q after caller mutation, want h2", got)
	}
}
