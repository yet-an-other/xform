package xrayconfig

import "slices"

// View is an immutable, ordered parse of the xray inbounds used to evaluate
// Connection profiles. Its accessors return copies, so callers cannot change
// the parse retained by the Watcher.
type View struct {
	inbounds []Inbound
}

// Inbounds returns every parsed inbound in xray config order.
func (v View) Inbounds() []Inbound {
	inbounds := make([]Inbound, len(v.inbounds))
	for index, inbound := range v.inbounds {
		inbounds[index] = inbound.clone()
	}
	return inbounds
}

// Inbound is one xray inbound and the User entries defined inside it.
type Inbound struct {
	Tag        string
	Protocol   string
	Port       string // listen port as written ("443", "1000-2000"), "" when absent
	Flow       string
	Decryption string
	Transport  TransportSettings
	Security   SecuritySettings

	users []InboundUser
}

// Users returns the inbound's email-bearing User entries in config order.
// Duplicate emails are retained because profile evaluation must reject the
// ambiguous inbound rather than choose one credential.
func (i Inbound) Users() []InboundUser {
	return slices.Clone(i.users)
}

func (i Inbound) clone() Inbound {
	i.users = slices.Clone(i.users)
	i.Security = i.Security.clone()
	return i
}

// DefaultFlow is the flow a newly attached client gets on this inbound
// (user-management spec §4): the first existing client's flow, or — with no
// clients to copy — xtls-rprx-vision on REALITY tcp/xhttp inbounds and empty
// elsewhere.
func DefaultFlow(inbound Inbound) string {
	if users := inbound.Users(); len(users) > 0 {
		return users[0].Flow
	}
	if inbound.Security.Type != "reality" {
		return ""
	}
	switch inbound.Transport.Type {
	case "tcp", "raw", "xhttp", "splithttp":
		return "xtls-rprx-vision"
	}
	return ""
}

// InboundUser is one email-bearing User entry from an inbound.
type InboundUser struct {
	Email    string
	ClientID string
	Flow     string
	Reverse  bool
}

// TransportSettings contains the server-side transport constraints used to
// validate direct Advertised connection settings. Type retains
// streamSettings.network as configured, including aliases and unsupported
// values.
type TransportSettings struct {
	Type                string
	RawHeaderConfigured bool
	RawHeaderType       string
	FinalMaskConfigured bool
	WebSocket           HTTPTransportSettings
	HTTPUpgrade         HTTPTransportSettings
	GRPC                GRPCTransportSettings
	XHTTP               XHTTPTransportSettings
}

// HTTPTransportSettings is the path and Host accepted by WebSocket or
// HTTPUpgrade.
type HTTPTransportSettings struct {
	Path string
	Host string
}

// GRPCTransportSettings is the server-side gRPC route accepted by xray.
type GRPCTransportSettings struct {
	ServiceName string
	Authority   string
	MultiMode   bool
}

// XHTTPTransportSettings is the server-side XHTTP route accepted by xray.
type XHTTPTransportSettings struct {
	Path            string
	Host            string
	Mode            string
	ExtraConfigured bool
}

// SecuritySettings contains the transport-security constraints used to
// validate direct Advertised connection settings. It omits certificate keys,
// REALITY private keys, and ML-DSA seeds.
type SecuritySettings struct {
	Type    string
	TLS     TLSSettings
	Reality RealitySettings
}

func (s SecuritySettings) clone() SecuritySettings {
	s.TLS.alpn = slices.Clone(s.TLS.alpn)
	s.Reality.serverNames = slices.Clone(s.Reality.serverNames)
	s.Reality.shortIDs = slices.Clone(s.Reality.shortIDs)
	return s
}

// TLSSettings contains public server constraints relevant to direct-profile
// validation. Certificate and key material is never retained.
type TLSSettings struct {
	ServerName       string
	RejectUnknownSNI bool

	alpn []string
}

// ALPN returns the configured TLS application protocols in config order.
func (s TLSSettings) ALPN() []string {
	return slices.Clone(s.alpn)
}

// RealitySettings contains only the names and short IDs accepted by the
// REALITY server. Private and derivable server material is never retained.
type RealitySettings struct {
	serverNames []string
	shortIDs    []string
}

// ServerNames returns the accepted REALITY server names in config order.
func (s RealitySettings) ServerNames() []string {
	return slices.Clone(s.serverNames)
}

// ShortIDs returns the accepted REALITY short IDs in config order. An empty
// short ID remains present because it is a valid explicit server choice.
func (s RealitySettings) ShortIDs() []string {
	return slices.Clone(s.shortIDs)
}
