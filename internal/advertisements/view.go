// Package advertisements loads the Panel's Advertised connection settings.
package advertisements

import "slices"

// Topology describes whether a User reaches xray directly or through a
// separate frontend.
type Topology string

const (
	TopologyDirect  Topology = "direct"
	TopologyFronted Topology = "fronted"
)

// TransportType is an advertised client transport.
type TransportType string

const (
	TransportTCP         TransportType = "tcp"
	TransportWebSocket   TransportType = "ws"
	TransportHTTPUpgrade TransportType = "httpupgrade"
	TransportGRPC        TransportType = "grpc"
	TransportXHTTP       TransportType = "xhttp"
)

// SecurityType is an advertised client transport-security type.
type SecurityType string

const (
	SecurityTLS     SecurityType = "tls"
	SecurityReality SecurityType = "reality"
	SecurityNone    SecurityType = "none"
)

// View is an immutable ordered parse of one Advertised connection settings
// document.
type View struct {
	advertisements []Advertisement
}

// Advertisements returns the records in document order. The returned values
// do not share mutable data with the View.
func (v View) Advertisements() []Advertisement {
	records := make([]Advertisement, len(v.advertisements))
	for index, record := range v.advertisements {
		records[index] = record.clone()
	}
	return records
}

// Advertisement is one inbound's advertised public client view. Invalid and
// duplicate records remain present so profile evaluation can make only the
// selected inbound unavailable.
type Advertisement struct {
	InboundTag string
	Name       string
	Topology   Topology
	Host       string
	Port       uint16
	Transport  Transport
	Security   Security

	validationError *ValidationError
	duplicate       bool
}

// ValidationError returns a safe record-local validation failure, or nil when
// the record is structurally valid.
func (a Advertisement) ValidationError() *ValidationError {
	if a.validationError == nil {
		return nil
	}
	problem := *a.validationError
	return &problem
}

// Duplicate reports whether another record selects the same non-empty inbound
// tag.
func (a Advertisement) Duplicate() bool {
	return a.duplicate
}

func (a Advertisement) clone() Advertisement {
	a.Transport.extra = slices.Clone(a.Transport.extra)
	a.Security.alpn = slices.Clone(a.Security.alpn)
	a.Security.certificatePins = slices.Clone(a.Security.certificatePins)
	if a.validationError != nil {
		problem := *a.validationError
		a.validationError = &problem
	}
	return a
}

// ValidationError is a safe explanation of an invalid advertisement. Messages
// contain field names but never configured values.
type ValidationError struct {
	Message string
}

// Transport is the typed client transport from one advertisement.
type Transport struct {
	Type        TransportType
	Path        string
	Host        string
	ServiceName string
	Mode        string
	Authority   string

	extra        []byte
	extraPresent bool
}

// Extra returns an immutable copy of the optional XHTTP extra object.
func (t Transport) Extra() ([]byte, bool) {
	return slices.Clone(t.extra), t.extraPresent
}

// Security is the typed client transport security from one advertisement.
type Security struct {
	Type              SecurityType
	Fingerprint       string
	ServerName        string
	ECH               string
	VerifyName        string
	PublicKey         string
	ShortID           string
	ShortIDPresent    bool
	PostQuantumVerify string
	SpiderX           string

	alpn            []string
	certificatePins []string
}

// ALPN returns a copy of the advertised TLS application protocols.
func (s Security) ALPN() []string {
	return slices.Clone(s.alpn)
}

// CertificatePins returns a copy of the advertised TLS certificate pins.
func (s Security) CertificatePins() []string {
	return slices.Clone(s.certificatePins)
}
