// Package xrayconfig reads the user roster from the local xray config file
// (SPEC.md §3): which users exist, and the protocol · security labels the
// users table shows. The panel parses the file instead of enabling xray's
// HandlerService — observing, never controlling.
package xrayconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// User is one config-defined user's table labels.
type User struct {
	Protocol string // inbound protocol, e.g. VLESS
	Security string // stream security (+ flow), e.g. XTLS-Reality
}

// config mirrors the inbound subset of the xray config file. Everything
// else — routing, outbounds, policy — is not the roster and stays unread.
type config struct {
	Inbounds []struct {
		Protocol string `json:"protocol"`
		Settings struct {
			Clients []struct {
				Email string `json:"email"`
				Flow  string `json:"flow"`
			} `json:"clients"`
		} `json:"settings"`
		StreamSettings struct {
			Security string `json:"security"`
		} `json:"streamSettings"`
	} `json:"inbounds"`
}

// Parse extracts the user roster from an xray config document: every entry
// in an inbound's `clients` list with an email, labeled by its inbound's
// protocol and security. Entries without an email have no identity and are
// skipped, as are inbounds without clients. An email listed on several
// inbounds keeps the first inbound's labels — config order decides.
func Parse(document []byte) (map[string]User, error) {
	// DisallowUnknownFields is deliberately NOT set: the xray config is
	// xray's format, and the roster must keep reading across its additions.
	decoder := json.NewDecoder(bytes.NewReader(document))
	var cfg config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse xray config: %w", err)
	}

	roster := make(map[string]User)
	for _, inbound := range cfg.Inbounds {
		if inbound.Protocol == "" {
			continue
		}
		protocol := strings.ToUpper(inbound.Protocol)
		for _, client := range inbound.Settings.Clients {
			if client.Email == "" {
				continue // no email, no identity (CONTEXT.md: the email IS the identity)
			}
			if _, exists := roster[client.Email]; exists {
				continue
			}
			roster[client.Email] = User{
				Protocol: protocol,
				Security: securityLabel(inbound.StreamSettings.Security, client.Flow),
			}
		}
	}
	return roster, nil
}

// securityLabel renders the protocol · security column's second half:
// the inbound's stream security, title-cased, with an XTLS- prefix when the
// client's flow is the vision family. No stream encryption reads "None" —
// never an empty label.
func securityLabel(security, flow string) string {
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
