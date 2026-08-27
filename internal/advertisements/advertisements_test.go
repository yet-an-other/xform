package advertisements_test

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/advertisements"
)

func TestParseLoadsAdvertisementWithDefaults(t *testing.T) {
	document := []byte(`{
		"version": 1,
		"advertisements": [{
			"inbound_tag": "vless-tls-main",
			"topology": "direct",
			"host": "edge.example.com",
			"port": 443,
			"transport": {"type": "tcp"},
			"security": {"type": "tls"}
		}]
	}`)

	view, err := advertisements.Parse(document)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	records := view.Advertisements()
	if got, want := len(records), 1; got != want {
		t.Fatalf("advertisements = %d, want %d", got, want)
	}
	record := records[0]
	if problem := record.ValidationError(); problem != nil {
		t.Fatalf("validation error = %+v, want valid advertisement", problem)
	}
	if record.InboundTag != "vless-tls-main" || record.Name != "vless-tls-main" ||
		record.Topology != advertisements.TopologyDirect {
		t.Errorf("identity = %+v, want tag/name vless-tls-main and direct topology", record)
	}
	if record.Host != "edge.example.com" || record.Port != 443 {
		t.Errorf("endpoint = %s:%d, want edge.example.com:443", record.Host, record.Port)
	}
	if record.Transport.Type != advertisements.TransportTCP {
		t.Errorf("transport = %+v, want tcp", record.Transport)
	}
	if record.Security.Type != advertisements.SecurityTLS || record.Security.Fingerprint != "chrome" ||
		record.Security.ServerName != "edge.example.com" {
		t.Errorf("security = %+v, want TLS with chrome and host defaults", record.Security)
	}
}

func TestParseRejectsStrictRootFailures(t *testing.T) {
	tests := []struct {
		name               string
		document           string
		unsupportedVersion bool
	}{
		{
			name:     "unknown root field",
			document: `{"version":1,"advertisements":[],"unknown":true}`,
		},
		{
			name:     "duplicate root field",
			document: `{"version":1,"version":1,"advertisements":[]}`,
		},
		{
			name: "duplicate nested field",
			document: `{
				"version":1,
				"advertisements":[{
					"inbound_tag":"main",
					"topology":"direct",
					"host":"edge.example.com",
					"port":443,
					"transport":{"type":"tcp","type":"tcp"},
					"security":{"type":"tls"}
				}]
			}`,
		},
		{
			name: "duplicate XHTTP extra field",
			document: `{
				"version":1,
				"advertisements":[{
					"inbound_tag":"main",
					"topology":"direct",
					"host":"edge.example.com",
					"port":443,
					"transport":{"type":"xhttp","path":"/x","host":"edge.example.com","mode":"auto","extra":{"nested":{"key":1,"key":2}}},
					"security":{"type":"tls"}
				}]
			}`,
		},
		{
			name:     "trailing JSON value",
			document: `{"version":1,"advertisements":[]} {}`,
		},
		{
			name:               "unsupported version",
			document:           `{"version":2,"advertisements":[]}`,
			unsupportedVersion: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := advertisements.Parse([]byte(test.document))
			if err == nil {
				t.Fatal("parse succeeded, want root failure")
			}
			if got := errors.Is(err, advertisements.ErrUnsupportedVersion); got != test.unsupportedVersion {
				t.Errorf("unsupported version = %t, want %t (error %v)", got, test.unsupportedVersion, err)
			}
		})
	}
}

func TestParseRetainsEverySupportedTypedShape(t *testing.T) {
	document, err := os.ReadFile("testdata/supported.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	view, err := advertisements.Parse(document)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	records := view.Advertisements()
	if got, want := len(records), 6; got != want {
		t.Fatalf("advertisements = %d, want %d", got, want)
	}
	for index, record := range records {
		if problem := record.ValidationError(); problem != nil {
			t.Errorf("advertisement %d (%q) validation error = %q", index, record.InboundTag, problem.Message)
		}
	}

	if got, want := []advertisements.TransportType{
		records[0].Transport.Type,
		records[1].Transport.Type,
		records[2].Transport.Type,
		records[3].Transport.Type,
		records[4].Transport.Type,
		records[5].Transport.Type,
	}, []advertisements.TransportType{
		advertisements.TransportTCP,
		advertisements.TransportWebSocket,
		advertisements.TransportHTTPUpgrade,
		advertisements.TransportGRPC,
		advertisements.TransportXHTTP,
		advertisements.TransportGRPC,
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("transport order = %v, want %v", got, want)
	}

	reality := records[0]
	if reality.Name != "Primary REALITY" || reality.Security.Type != advertisements.SecurityReality ||
		reality.Security.Fingerprint != "firefox" || reality.Security.ServerName != "www.microsoft.com" ||
		reality.Security.PublicKey != "public-key" || !reality.Security.ShortIDPresent || reality.Security.ShortID != "" ||
		reality.Security.PostQuantumVerify != "verify-key" || reality.Security.SpiderX != "/news" {
		t.Errorf("REALITY advertisement = %+v", reality)
	}

	websocket := records[1]
	if websocket.Topology != advertisements.TopologyFronted || websocket.Transport.Path != "/socket" ||
		websocket.Transport.Host != "origin.example.com" || websocket.Security.ECH != "ech-value" ||
		websocket.Security.VerifyName != "verify.example.com" {
		t.Errorf("WebSocket/TLS advertisement = %+v", websocket)
	}
	if got, want := websocket.Security.ALPN(), []string{"h2", "http/1.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("TLS ALPN = %v, want %v", got, want)
	}
	if got, want := websocket.Security.CertificatePins(), []string{"pin-one", "pin-two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("TLS certificate pins = %v, want %v", got, want)
	}

	if got := records[2].Transport; got.Path != "/upgrade" || got.Host != "upgrade.example.com" {
		t.Errorf("HTTPUpgrade transport = %+v", got)
	}
	if got := records[3].Transport; got.ServiceName != "xform.Profile" || got.Mode != "multi" || got.Authority != "" {
		t.Errorf("gRPC transport = %+v", got)
	}
	if records[3].Security.Fingerprint != "chrome" {
		t.Errorf("default REALITY fingerprint = %q, want chrome", records[3].Security.Fingerprint)
	}
	extra, present := records[4].Transport.Extra()
	if !present || len(extra) == 0 || records[4].Transport.Mode != "stream-up" {
		t.Errorf("XHTTP transport = %+v, extra present %t", records[4].Transport, present)
	}
	if records[5].Security.Type != advertisements.SecurityNone {
		t.Errorf("none security = %+v", records[5].Security)
	}
}

func parseSingleAdvertisement(t *testing.T, advertisement string) advertisements.Advertisement {
	t.Helper()
	document := fmt.Sprintf(`{"version":1,"advertisements":[%s]}`, advertisement)
	view, err := advertisements.Parse([]byte(document))
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	records := view.Advertisements()
	if len(records) != 1 {
		t.Fatalf("advertisements = %d, want 1", len(records))
	}
	return records[0]
}

func advertisementWith(transport, security string) string {
	return fmt.Sprintf(`{
		"inbound_tag":"main",
		"topology":"direct",
		"host":"edge.example.com",
		"port":443,
		"transport":%s,
		"security":%s
	}`, transport, security)
}

func TestParseRejectsInvalidTypedRecordFields(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		security  string
	}{
		{name: "missing transport type", transport: `{}`, security: `{"type":"tls"}`},
		{name: "TCP field", transport: `{"type":"tcp","path":"/"}`, security: `{"type":"tls"}`},
		{name: "WebSocket path", transport: `{"type":"ws","path":"","host":"edge.example.com"}`, security: `{"type":"tls"}`},
		{name: "WebSocket host", transport: `{"type":"ws","path":"/ws"}`, security: `{"type":"tls"}`},
		{name: "HTTPUpgrade path", transport: `{"type":"httpupgrade","host":"edge.example.com"}`, security: `{"type":"tls"}`},
		{name: "gRPC service", transport: `{"type":"grpc","service_name":""}`, security: `{"type":"tls"}`},
		{name: "gRPC mode", transport: `{"type":"grpc","service_name":"service","mode":"invalid"}`, security: `{"type":"tls"}`},
		{name: "gRPC authority JSON type", transport: `{"type":"grpc","service_name":"service","authority":null}`, security: `{"type":"tls"}`},
		{name: "XHTTP mode missing", transport: `{"type":"xhttp","path":"/x","host":"edge.example.com"}`, security: `{"type":"tls"}`},
		{name: "XHTTP mode invalid", transport: `{"type":"xhttp","path":"/x","host":"edge.example.com","mode":"invalid"}`, security: `{"type":"tls"}`},
		{name: "XHTTP extra array", transport: `{"type":"xhttp","path":"/x","host":"edge.example.com","mode":"auto","extra":[]}`, security: `{"type":"tls"}`},
		{name: "XHTTP extra number outside JCS range", transport: `{"type":"xhttp","path":"/x","host":"edge.example.com","mode":"auto","extra":{"value":1e9999}}`, security: `{"type":"tls"}`},
		{name: "XHTTP extra lone surrogate", transport: `{"type":"xhttp","path":"/x","host":"edge.example.com","mode":"auto","extra":{"value":"\ud800"}}`, security: `{"type":"tls"}`},
		{name: "TLS fingerprint", transport: `{"type":"tcp"}`, security: `{"type":"tls","fingerprint":""}`},
		{name: "TLS optional JSON type", transport: `{"type":"tcp"}`, security: `{"type":"tls","ech":null}`},
		{name: "TLS ALPN type", transport: `{"type":"tcp"}`, security: `{"type":"tls","alpn":"h2"}`},
		{name: "TLS ALPN empty value", transport: `{"type":"tcp"}`, security: `{"type":"tls","alpn":[""]}`},
		{name: "TLS certificate pin", transport: `{"type":"tcp"}`, security: `{"type":"tls","certificate_pins":[""]}`},
		{name: "TLS unknown field", transport: `{"type":"tcp"}`, security: `{"type":"tls","unknown":true}`},
		{name: "REALITY server name", transport: `{"type":"tcp"}`, security: `{"type":"reality","public_key":"key","short_id":"id"}`},
		{name: "REALITY public key", transport: `{"type":"tcp"}`, security: `{"type":"reality","server_name":"example.com","short_id":"id"}`},
		{name: "REALITY short ID missing", transport: `{"type":"tcp"}`, security: `{"type":"reality","server_name":"example.com","public_key":"key"}`},
		{name: "none field", transport: `{"type":"tcp"}`, security: `{"type":"none","fingerprint":"chrome"}`},
		{name: "unsupported transport field", transport: `{"type":"future","value":true}`, security: `{"type":"tls"}`},
		{name: "unsupported security field", transport: `{"type":"tcp"}`, security: `{"type":"future","value":true}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := parseSingleAdvertisement(t, advertisementWith(test.transport, test.security))
			if record.ValidationError() == nil {
				t.Errorf("record = %+v, want validation error", record)
			}
		})
	}
}

func TestParseValidatesAdvertisementEnvelope(t *testing.T) {
	validTransport := `{"type":"tcp"}`
	validSecurity := `{"type":"tls"}`
	tests := []struct {
		name          string
		advertisement string
	}{
		{name: "missing inbound tag", advertisement: `{"topology":"direct","host":"edge.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "empty name", advertisement: `{"inbound_tag":"main","name":"","topology":"direct","host":"edge.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "invalid topology", advertisement: `{"inbound_tag":"main","topology":"sideways","host":"edge.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "host scheme", advertisement: `{"inbound_tag":"main","topology":"direct","host":"https://edge.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "host path", advertisement: `{"inbound_tag":"main","topology":"direct","host":"edge.example.com/path","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "host trailing dot", advertisement: `{"inbound_tag":"main","topology":"direct","host":"edge.example.com.","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "host mapped trailing dot", advertisement: `{"inbound_tag":"main","topology":"direct","host":"edge.example。","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "host label too long", advertisement: fmt.Sprintf(`{"inbound_tag":"main","topology":"direct","host":%q,"port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`, strings.Repeat("a", 64)+".example")},
		{name: "host name too long", advertisement: fmt.Sprintf(`{"inbound_tag":"main","topology":"direct","host":%q,"port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`, strings.Repeat("a", 63)+"."+strings.Repeat("b", 63)+"."+strings.Repeat("c", 63)+"."+strings.Repeat("d", 63))},
		{name: "IPv6 zone", advertisement: `{"inbound_tag":"main","topology":"direct","host":"fe80::1%eth0","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "bracketed IPv4", advertisement: `{"inbound_tag":"main","topology":"direct","host":"[192.0.2.1]","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "zero port", advertisement: `{"inbound_tag":"main","topology":"direct","host":"edge.example.com","port":0,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "large port", advertisement: `{"inbound_tag":"main","topology":"direct","host":"edge.example.com","port":65536,"transport":{"type":"tcp"},"security":{"type":"tls"}}`},
		{name: "unknown field", advertisement: `{"inbound_tag":"main","topology":"direct","host":"edge.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"},"unknown":true}`},
		{name: "missing transport", advertisement: fmt.Sprintf(`{"inbound_tag":"main","topology":"direct","host":"edge.example.com","port":443,"security":%s}`, validSecurity)},
		{name: "missing security", advertisement: fmt.Sprintf(`{"inbound_tag":"main","topology":"direct","host":"edge.example.com","port":443,"transport":%s}`, validTransport)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := parseSingleAdvertisement(t, test.advertisement)
			if record.ValidationError() == nil {
				t.Errorf("record = %+v, want validation error", record)
			}
		})
	}
}

func TestParseAcceptsDomainAndIPHostForms(t *testing.T) {
	for _, host := range []string{"edge.example.com", "bücher.example", "192.0.2.1", "2001:db8::1", "[2001:db8::1]"} {
		t.Run(host, func(t *testing.T) {
			advertisement := fmt.Sprintf(`{
				"inbound_tag":"main",
				"topology":"direct",
				"host":%q,
				"port":443,
				"transport":{"type":"tcp"},
				"security":{"type":"tls"}
			}`, host)
			record := parseSingleAdvertisement(t, advertisement)
			if problem := record.ValidationError(); problem != nil {
				t.Errorf("host %q validation error = %q", host, problem.Message)
			}
		})
	}
}

func TestParseNormalizesOptionalEmptyValues(t *testing.T) {
	record := parseSingleAdvertisement(t, advertisementWith(
		`{"type":"grpc","service_name":"service","mode":"","authority":""}`,
		`{"type":"tls","server_name":"","alpn":[],"ech":"","certificate_pins":[],"verify_name":""}`,
	))
	if problem := record.ValidationError(); problem != nil {
		t.Fatalf("validation error = %q", problem.Message)
	}
	if record.Transport.Mode != "gun" || record.Transport.Authority != "" {
		t.Errorf("gRPC defaults = %+v, want gun mode and empty authority", record.Transport)
	}
	if record.Security.Fingerprint != "chrome" || record.Security.ServerName != "edge.example.com" ||
		record.Security.ECH != "" || record.Security.VerifyName != "" ||
		len(record.Security.ALPN()) != 0 || len(record.Security.CertificatePins()) != 0 {
		t.Errorf("TLS defaults = %+v", record.Security)
	}
}

func TestViewAccessorsDoNotMutateRetainedAdvertisements(t *testing.T) {
	document, err := os.ReadFile("testdata/supported.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	view, err := advertisements.Parse(document)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	records := view.Advertisements()
	records[0].Name = "changed"
	alpn := records[1].Security.ALPN()
	alpn[0] = "changed"
	pins := records[1].Security.CertificatePins()
	pins[0] = "changed"
	extra, _ := records[4].Transport.Extra()
	extra[0] = 'x'

	fresh := view.Advertisements()
	if fresh[0].Name != "Primary REALITY" || fresh[1].Security.ALPN()[0] != "h2" ||
		fresh[1].Security.CertificatePins()[0] != "pin-one" {
		t.Errorf("fresh advertisements changed through an accessor: %+v", fresh)
	}
	freshExtra, _ := fresh[4].Transport.Extra()
	if freshExtra[0] == 'x' {
		t.Error("fresh XHTTP extra changed through an accessor")
	}
}

func TestInvalidRecordIdentityDoesNotDependOnFieldOrder(t *testing.T) {
	for _, advertisement := range []string{
		`{"name":123,"inbound_tag":"retained","topology":"direct","host":"edge.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`,
		`{"inbound_tag":"retained","name":123,"topology":"direct","host":"edge.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`,
	} {
		record := parseSingleAdvertisement(t, advertisement)
		if record.ValidationError() == nil {
			t.Fatal("record with numeric name has no validation error")
		}
		if record.InboundTag != "retained" || record.Name != "retained" {
			t.Errorf("invalid record identity = tag %q, name %q; want retained", record.InboundTag, record.Name)
		}
	}
}

func TestParseKeepsInvalidAndDuplicateRecordsLocalToTheirInbound(t *testing.T) {
	document, err := os.ReadFile("testdata/independent-records.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	view, err := advertisements.Parse(document)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	records := view.Advertisements()
	if got, want := len(records), 6; got != want {
		t.Fatalf("advertisements = %d, want %d", got, want)
	}
	if problem := records[0].ValidationError(); problem != nil || records[0].Duplicate() {
		t.Errorf("healthy record = %+v, problem %+v", records[0], problem)
	}
	problem := records[1].ValidationError()
	if problem == nil {
		t.Fatal("broken record has no validation error")
	}
	if records[1].InboundTag != "broken" || records[1].Name != "Broken" {
		t.Errorf("broken identity = %+v, want retained tag and name", records[1])
	}
	if strings.Contains(problem.Message, "secret-value-must-not-leak") {
		t.Errorf("validation message leaked configured value: %q", problem.Message)
	}
	if !records[2].Duplicate() || !records[3].Duplicate() {
		t.Errorf("duplicate flags = %t, %t, want both true", records[2].Duplicate(), records[3].Duplicate())
	}
	if problem := records[2].ValidationError(); problem != nil {
		t.Errorf("first duplicate also invalid = %+v", problem)
	}
	if problem := records[3].ValidationError(); problem == nil {
		t.Error("second duplicate lost its independent validation error")
	}
	if problem := records[4].ValidationError(); problem != nil || records[4].Transport.Type != "future" {
		t.Errorf("future transport = %+v, problem %+v", records[4].Transport, problem)
	}
	if problem := records[5].ValidationError(); problem != nil || records[5].Security.Type != "future" {
		t.Errorf("future security = %+v, problem %+v", records[5].Security, problem)
	}
}
