package api

import (
	"encoding/json"
	"strings"

	"github.com/yet-an-other/xform/internal/profiles"
	"github.com/yet-an-other/xform/internal/users"
)

type userDetailResponse struct {
	CollectedAt        int64                      `json:"collected_at"`
	Stale              bool                       `json:"stale"`
	User               users.User                 `json:"user"`
	ConnectionProfiles connectionProfilesResponse `json:"connection_profiles"`
}

type connectionProfilesResponse struct {
	State    profiles.State                 `json:"state"`
	LoadedAt *int64                         `json:"loaded_at"`
	Stale    bool                           `json:"stale"`
	Errors   []connectionProfileSourceError `json:"errors"`
	Items    []any                          `json:"items"`
}

type connectionProfileSourceError struct {
	Source  profiles.Source `json:"source"`
	Reason  string          `json:"reason"`
	Message string          `json:"message"`
}

type availableConnectionProfile struct {
	Status     profiles.Status  `json:"status"`
	InboundTag string           `json:"inbound_tag"`
	Name       string           `json:"name"`
	Topology   string           `json:"topology"`
	ClientID   string           `json:"client_id"`
	Flow       *string          `json:"flow"`
	Endpoint   profileEndpoint  `json:"endpoint"`
	Transport  profileTransport `json:"transport"`
	Security   profileSecurity  `json:"security"`
	URI        string           `json:"uri"`
}

type unavailableConnectionProfile struct {
	Status     profiles.Status `json:"status"`
	InboundTag *string         `json:"inbound_tag"`
	Name       *string         `json:"name"`
	Reason     profiles.Reason `json:"reason"`
	Message    string          `json:"message"`
}

type profileEndpoint struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

type profileTransport struct {
	Type        string          `json:"type"`
	Path        string          `json:"path,omitempty"`
	Host        string          `json:"host,omitempty"`
	ServiceName string          `json:"service_name,omitempty"`
	Mode        string          `json:"mode,omitempty"`
	Authority   string          `json:"authority,omitempty"`
	Extra       json.RawMessage `json:"extra,omitempty"`
}

type profileSecurity struct {
	Type              string   `json:"type"`
	Fingerprint       string   `json:"fingerprint,omitempty"`
	ServerName        string   `json:"server_name,omitempty"`
	ALPN              []string `json:"alpn,omitempty"`
	ECH               string   `json:"ech,omitempty"`
	CertificatePins   []string `json:"certificate_pins,omitempty"`
	VerifyName        string   `json:"verify_name,omitempty"`
	PublicKey         string   `json:"public_key,omitempty"`
	ShortID           *string  `json:"short_id,omitempty"`
	PostQuantumVerify string   `json:"post_quantum_verify,omitempty"`
	SpiderX           string   `json:"spider_x,omitempty"`
}

func malformedUserEmailEscape(requestTarget string) bool {
	path, _, _ := strings.Cut(requestTarget, "?")
	const prefix = "/api/v1/users/"
	start := strings.Index(path, prefix)
	if start == -1 {
		return false
	}
	segment := path[start+len(prefix):]
	for index := 0; index < len(segment); index++ {
		if segment[index] != '%' {
			continue
		}
		if index+2 >= len(segment) || !isHex(segment[index+1]) || !isHex(segment[index+2]) {
			return true
		}
		index += 2
	}
	return false
}

func isHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func connectionProfilesJSON(collection profiles.Collection) connectionProfilesResponse {
	response := connectionProfilesResponse{
		State:  collection.State,
		Stale:  collection.Stale,
		Errors: make([]connectionProfileSourceError, 0, len(collection.Errors)),
		Items:  make([]any, 0, len(collection.Items)),
	}
	if collection.LoadedAt != nil {
		loadedAt := collection.LoadedAt.Unix()
		response.LoadedAt = &loadedAt
	}
	for _, sourceError := range collection.Errors {
		response.Errors = append(response.Errors, connectionProfileSourceError{
			Source: sourceError.Source, Reason: sourceError.Reason, Message: sourceError.Message,
		})
	}
	for _, item := range collection.Items {
		if item.Available != nil {
			response.Items = append(response.Items, availableConnectionProfileJSON(*item.Available))
			continue
		}
		if item.Unavailable != nil {
			response.Items = append(response.Items, unavailableConnectionProfile{
				Status: profiles.StatusUnavailable, InboundTag: item.Unavailable.InboundTag,
				Name: item.Unavailable.Name, Reason: item.Unavailable.Reason, Message: item.Unavailable.Message,
			})
		}
	}
	return response
}

func availableConnectionProfileJSON(profile profiles.Available) availableConnectionProfile {
	var shortID *string
	if profile.Security.ShortIDPresent {
		value := profile.Security.ShortID
		shortID = &value
	}
	return availableConnectionProfile{
		Status: profiles.StatusAvailable, InboundTag: profile.InboundTag, Name: profile.Name,
		Topology: string(profile.Topology), ClientID: profile.ClientID, Flow: profile.Flow,
		Endpoint: profileEndpoint{Host: profile.Endpoint.Host, Port: profile.Endpoint.Port},
		Transport: profileTransport{
			Type: string(profile.Transport.Type), Path: profile.Transport.Path, Host: profile.Transport.Host,
			ServiceName: profile.Transport.ServiceName, Mode: profile.Transport.Mode,
			Authority: profile.Transport.Authority, Extra: profile.Transport.Extra,
		},
		Security: profileSecurity{
			Type: string(profile.Security.Type), Fingerprint: profile.Security.Fingerprint,
			ServerName: profile.Security.ServerName, ALPN: profile.Security.ALPN, ECH: profile.Security.ECH,
			CertificatePins: profile.Security.CertificatePins, VerifyName: profile.Security.VerifyName,
			PublicKey: profile.Security.PublicKey, ShortID: shortID,
			PostQuantumVerify: profile.Security.PostQuantumVerify, SpiderX: profile.Security.SpiderX,
		},
		URI: profile.URI,
	}
}
