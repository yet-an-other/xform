// Package xrayconfig reads the Roster and Connection profile candidates from
// the local xray config file. The Panel parses the file instead of enabling
// xray's HandlerService. It observes xray and never controls it.
package xrayconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// User is one config-defined user's table labels.
type User struct {
	Protocol string // inbound protocol, e.g. VLESS
	Security string // stream security (+ flow), e.g. XTLS-Reality
}

// Client is one config-defined VLESS client: the credential and the tags of
// the VLESS inbounds the user is attached to, in config order. These are the
// clients the panel's roster store adopts (user-management spec §4); only
// VLESS inbounds feed it, because VLESS is what the panel manages.
type Client struct {
	ClientID string
	Inbounds []string
}

// RosterParse is one config parse's roster hand-off: per-user table labels,
// plus the VLESS clients awaiting adoption into the panel-held Roster.
type RosterParse struct {
	Labels  map[string]User
	Clients map[string]Client
}

// config mirrors only the xray inbound fields used by the Roster and profile
// evaluation. Routing, outbounds, policy, and private server material stay
// unread. Unknown fields remain accepted because this is xray's format.
type config struct {
	Inbounds []inboundConfig `json:"inbounds"`
}

type inboundConfig struct {
	Tag            string               `json:"tag"`
	Protocol       string               `json:"protocol"`
	Port           json.RawMessage      `json:"port"`
	Settings       inboundSettings      `json:"settings"`
	StreamSettings streamSettingsConfig `json:"streamSettings"`
}

type inboundSettings struct {
	Clients    []clientConfig `json:"clients"`
	Flow       string         `json:"flow"`
	Decryption string         `json:"decryption"`
}

type clientConfig struct {
	Email   string          `json:"email"`
	ID      string          `json:"id"`
	Flow    string          `json:"flow"`
	Reverse json.RawMessage `json:"reverse"`
}

type streamSettingsConfig struct {
	Network     string              `json:"network"`
	Security    string              `json:"security"`
	FinalMask   json.RawMessage     `json:"finalmask"`
	TLS         tlsConfig           `json:"tlsSettings"`
	Reality     realityConfig       `json:"realitySettings"`
	RAW         *rawConfig          `json:"rawSettings"`
	TCP         *rawConfig          `json:"tcpSettings"`
	WebSocket   httpTransportConfig `json:"wsSettings"`
	HTTPUpgrade httpTransportConfig `json:"httpupgradeSettings"`
	GRPC        grpcConfig          `json:"grpcSettings"`
	XHTTP       *xhttpConfig        `json:"xhttpSettings"`
	SplitHTTP   *xhttpConfig        `json:"splithttpSettings"`
}

type rawConfig struct {
	Header json.RawMessage `json:"header"`
}

type httpTransportConfig struct {
	Path    string            `json:"path"`
	Host    string            `json:"host"`
	Headers map[string]string `json:"headers"`
}

type grpcConfig struct {
	ServiceName string `json:"serviceName"`
	Authority   string `json:"authority"`
	MultiMode   bool   `json:"multiMode"`
}

type xhttpConfig struct {
	Path  string          `json:"path"`
	Host  string          `json:"host"`
	Mode  string          `json:"mode"`
	Extra json.RawMessage `json:"extra"`
}

type tlsConfig struct {
	ServerName       string     `json:"serverName"`
	ALPN             stringList `json:"alpn"`
	RejectUnknownSNI bool       `json:"rejectUnknownSni"`
}

type realityConfig struct {
	ServerNames []string `json:"serverNames"`
	ShortIDs    []string `json:"shortIds"`
}

type stringList []string

func (values *stringList) UnmarshalJSON(document []byte) error {
	var list []string
	if err := json.Unmarshal(document, &list); err == nil {
		*values = list
		return nil
	}
	var value string
	if err := json.Unmarshal(document, &value); err == nil {
		*values = strings.Split(value, ",")
		return nil
	}
	return fmt.Errorf("parse xray string list: unsupported value")
}

// Parse extracts the existing user Roster from an xray config document.
// Entries without an email have no identity and are skipped. An email listed
// on several inbounds keeps the first inbound's labels, so config order still
// decides the table label exactly as before.
func Parse(document []byte) (map[string]User, error) {
	cfg, err := decode(document)
	if err != nil {
		return nil, err
	}
	return buildRosterParse(cfg).Labels, nil
}

// ParseView extracts the immutable, ordered inbound view used for Connection
// profile evaluation.
func ParseView(document []byte) (View, error) {
	cfg, err := decode(document)
	if err != nil {
		return View{}, err
	}
	return buildView(cfg), nil
}

func decode(document []byte) (config, error) {
	// Unknown fields stay accepted because this is xray's format, but the
	// document must contain exactly one complete JSON value.
	var cfg config
	if err := json.Unmarshal(document, &cfg); err != nil {
		return config{}, fmt.Errorf("parse xray config: %w", err)
	}
	return cfg, nil
}

func parse(document []byte) (RosterParse, View, error) {
	cfg, err := decode(document)
	if err != nil {
		return RosterParse{}, View{}, err
	}
	return buildRosterParse(cfg), buildView(cfg), nil
}

// buildRosterParse walks the inbounds once, building both halves of the
// roster hand-off. The labels cover every protocol, and an email listed on
// several inbounds keeps the first inbound's labels — config order decides,
// so the table label is stable. The clients are the roster store's adoption
// source and cover VLESS only, because VLESS is what the panel manages: the
// first inbound's Client ID wins exactly as for labels, and every tagged
// attachment is gathered in config order. A client whose VLESS inbounds are
// all untagged is still adopted — with zero attachments, a profile-less user
// (user-management spec §3). Clients without an email have no identity and
// are skipped (CONTEXT.md: the email IS the identity).
func buildRosterParse(cfg config) RosterParse {
	labels := make(map[string]User)
	clients := make(map[string]Client)
	for _, inbound := range cfg.Inbounds {
		if inbound.Protocol == "" {
			continue
		}
		protocol := strings.ToUpper(inbound.Protocol)
		for _, client := range inbound.Settings.Clients {
			if client.Email == "" {
				continue
			}
			if _, exists := labels[client.Email]; !exists {
				labels[client.Email] = User{
					Protocol: protocol,
					Security: SecurityLabel(inbound.StreamSettings.Security, client.Flow),
				}
			}
			if protocol != "VLESS" {
				continue
			}
			collected, exists := clients[client.Email]
			if !exists {
				collected.ClientID = client.ID
			}
			if inbound.Tag != "" && !slices.Contains(collected.Inbounds, inbound.Tag) {
				collected.Inbounds = append(collected.Inbounds, inbound.Tag)
			}
			clients[client.Email] = collected
		}
	}
	return RosterParse{Labels: labels, Clients: clients}
}

func buildView(cfg config) View {
	view := View{inbounds: make([]Inbound, len(cfg.Inbounds))}
	for index, source := range cfg.Inbounds {
		raw := source.StreamSettings.TCP
		if source.StreamSettings.RAW != nil {
			raw = source.StreamSettings.RAW
		}
		xhttp := source.StreamSettings.SplitHTTP
		if source.StreamSettings.XHTTP != nil {
			xhttp = source.StreamSettings.XHTTP
		}

		inbound := Inbound{
			Tag:        source.Tag,
			Protocol:   source.Protocol,
			Port:       portLabel(source.Port),
			Flow:       source.Settings.Flow,
			Decryption: source.Settings.Decryption,
			Transport: TransportSettings{
				Type:                source.StreamSettings.Network,
				FinalMaskConfigured: configured(source.StreamSettings.FinalMask),
				WebSocket: HTTPTransportSettings{
					Path: source.StreamSettings.WebSocket.Path,
					Host: effectiveWebSocketHost(source.StreamSettings.WebSocket),
				},
				HTTPUpgrade: HTTPTransportSettings{
					Path: source.StreamSettings.HTTPUpgrade.Path,
					Host: source.StreamSettings.HTTPUpgrade.Host,
				},
				GRPC: GRPCTransportSettings{
					ServiceName: source.StreamSettings.GRPC.ServiceName,
					Authority:   source.StreamSettings.GRPC.Authority,
					MultiMode:   source.StreamSettings.GRPC.MultiMode,
				},
			},
			Security: SecuritySettings{
				Type: source.StreamSettings.Security,
				TLS: TLSSettings{
					ServerName:       source.StreamSettings.TLS.ServerName,
					RejectUnknownSNI: source.StreamSettings.TLS.RejectUnknownSNI,
					alpn:             append([]string(nil), source.StreamSettings.TLS.ALPN...),
				},
				Reality: RealitySettings{
					serverNames: append([]string(nil), source.StreamSettings.Reality.ServerNames...),
					shortIDs:    append([]string(nil), source.StreamSettings.Reality.ShortIDs...),
				},
			},
		}
		if raw != nil {
			inbound.Transport.RawHeaderConfigured = configured(raw.Header)
			inbound.Transport.RawHeaderType = rawHeaderType(raw.Header)
		}
		if xhttp != nil {
			inbound.Transport.XHTTP = XHTTPTransportSettings{
				Path:            xhttp.Path,
				Host:            xhttp.Host,
				Mode:            xhttp.Mode,
				ExtraConfigured: configured(xhttp.Extra),
			}
		}
		for _, sourceUser := range source.Settings.Clients {
			if sourceUser.Email == "" {
				continue
			}
			inbound.users = append(inbound.users, InboundUser{
				Email:    sourceUser.Email,
				ClientID: sourceUser.ID,
				Flow:     sourceUser.Flow,
				Reverse:  configured(sourceUser.Reverse),
			})
		}
		view.inbounds[index] = inbound
	}
	return view
}

func configured(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null"))
}

// portLabel renders the inbound's listen port for the add-user dialog's
// inbound options. xray accepts a number or a range string; either is kept
// as written. An absent or unparsable port renders empty.
func portLabel(raw json.RawMessage) string {
	if !configured(raw) {
		return ""
	}
	var port int
	if err := json.Unmarshal(raw, &port); err == nil {
		return strconv.Itoa(port)
	}
	var portRange string
	if err := json.Unmarshal(raw, &portRange); err == nil {
		return portRange
	}
	return ""
}

func rawHeaderType(document json.RawMessage) string {
	if !configured(document) {
		return ""
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(document, &header); err != nil {
		return ""
	}
	return header.Type
}

func effectiveWebSocketHost(settings httpTransportConfig) string {
	if settings.Host != "" {
		return settings.Host
	}
	for name, value := range settings.Headers {
		if strings.EqualFold(name, "host") {
			return value
		}
	}
	return ""
}

// SecurityLabel renders the protocol · security column's second half:
// the inbound's stream security, title-cased, with an XTLS- prefix when the
// client's flow is the vision family. No stream encryption reads "None" —
// never an empty label.
func SecurityLabel(security, flow string) string {
	var label string
	switch strings.ToLower(security) {
	case "", "none":
		label = "None"
	case "tls":
		label = "TLS"
	default: // reality, and whatever xray adds next
		runes := []rune(security)
		runes[0] = unicode.ToUpper(runes[0])
		label = string(runes)
	}
	if label != "None" && strings.HasPrefix(strings.ToLower(flow), "xtls") {
		label = "XTLS-" + label
	}
	return label
}
