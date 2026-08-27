package advertisements

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// ErrUnsupportedVersion reports a well-formed root with a schema version the
// Panel does not implement.
var ErrUnsupportedVersion = errors.New("unsupported Advertised connection settings version")

type rootDocument struct {
	Version        *int               `json:"version"`
	Advertisements *[]json.RawMessage `json:"advertisements"`
}

type advertisementDocument struct {
	InboundTag stringField     `json:"inbound_tag"`
	Name       stringField     `json:"name"`
	Topology   stringField     `json:"topology"`
	Host       stringField     `json:"host"`
	Port       intField        `json:"port"`
	Transport  json.RawMessage `json:"transport"`
	Security   json.RawMessage `json:"security"`
}

type stringField struct {
	value string
	set   bool
}

func (field *stringField) UnmarshalJSON(document []byte) error {
	field.set = true
	if bytes.Equal(bytes.TrimSpace(document), []byte("null")) {
		return errors.New("value must be a string")
	}
	if err := json.Unmarshal(document, &field.value); err != nil {
		return errors.New("value must be a string")
	}
	return nil
}

type intField struct {
	value int
	set   bool
}

func (field *intField) UnmarshalJSON(document []byte) error {
	field.set = true
	if err := json.Unmarshal(document, &field.value); err != nil {
		return errors.New("value must be an integer")
	}
	return nil
}

type stringArrayField struct {
	values []string
	set    bool
}

func (field *stringArrayField) UnmarshalJSON(document []byte) error {
	field.set = true
	if bytes.Equal(bytes.TrimSpace(document), []byte("null")) {
		return errors.New("value must be an array of strings")
	}
	if err := json.Unmarshal(document, &field.values); err != nil {
		return errors.New("value must be an array of strings")
	}
	for _, value := range field.values {
		if value == "" {
			return errors.New("array strings must not be empty")
		}
	}
	return nil
}

type httpTransportDocument struct {
	Type stringField `json:"type"`
	Path stringField `json:"path"`
	Host stringField `json:"host"`
}

type grpcTransportDocument struct {
	Type        stringField `json:"type"`
	ServiceName stringField `json:"service_name"`
	Mode        stringField `json:"mode"`
	Authority   stringField `json:"authority"`
}

type xhttpTransportDocument struct {
	Type  stringField     `json:"type"`
	Path  stringField     `json:"path"`
	Host  stringField     `json:"host"`
	Mode  stringField     `json:"mode"`
	Extra json.RawMessage `json:"extra"`
}

type tlsSecurityDocument struct {
	Type            stringField      `json:"type"`
	Fingerprint     stringField      `json:"fingerprint"`
	ServerName      stringField      `json:"server_name"`
	ALPN            stringArrayField `json:"alpn"`
	ECH             stringField      `json:"ech"`
	CertificatePins stringArrayField `json:"certificate_pins"`
	VerifyName      stringField      `json:"verify_name"`
}

type realitySecurityDocument struct {
	Type              stringField `json:"type"`
	Fingerprint       stringField `json:"fingerprint"`
	ServerName        stringField `json:"server_name"`
	PublicKey         stringField `json:"public_key"`
	ShortID           stringField `json:"short_id"`
	PostQuantumVerify stringField `json:"post_quantum_verify"`
	SpiderX           stringField `json:"spider_x"`
}

// Parse validates the strict document root and then validates each
// advertisement independently.
func Parse(document []byte) (View, error) {
	if err := validateJSONDocument(document); err != nil {
		return View{}, fmt.Errorf("parse Advertised connection settings root: %w", err)
	}
	var root rootDocument
	if err := decodeStrict(document, &root); err != nil {
		return View{}, fmt.Errorf("parse Advertised connection settings root: %w", err)
	}
	if root.Version == nil {
		return View{}, errors.New("parse Advertised connection settings root: version is required")
	}
	if *root.Version != 1 {
		return View{}, ErrUnsupportedVersion
	}
	if root.Advertisements == nil {
		return View{}, errors.New("parse Advertised connection settings root: advertisements is required")
	}

	view := View{advertisements: make([]Advertisement, len(*root.Advertisements))}
	for index, raw := range *root.Advertisements {
		view.advertisements[index] = parseAdvertisement(raw)
	}
	markDuplicates(view.advertisements)
	return view, nil
}

func parseAdvertisement(document []byte) Advertisement {
	identity := advertisementIdentity(document)
	var raw advertisementDocument
	if err := decodeStrict(document, &raw); err != nil {
		return invalidAdvertisement(identity, "Advertisement fields must use the documented names and JSON types.")
	}

	record := Advertisement{
		InboundTag: raw.InboundTag.value,
		Name:       raw.Name.value,
		Topology:   Topology(raw.Topology.value),
		Host:       raw.Host.value,
	}
	if record.Name == "" {
		record.Name = record.InboundTag
	}
	if !raw.InboundTag.set || record.InboundTag == "" {
		return invalidate(record, "inbound_tag is required and must not be empty.")
	}
	if raw.Name.set && raw.Name.value == "" {
		return invalidate(record, "name must not be empty when present.")
	}
	if !raw.Topology.set || (record.Topology != TopologyDirect && record.Topology != TopologyFronted) {
		return invalidate(record, "topology must be direct or fronted.")
	}
	if !raw.Host.set || !validAdvertisedHost(record.Host) {
		return invalidate(record, "host must be a domain, IPv4 address, or IPv6 address without a scheme or path.")
	}
	if !raw.Port.set || raw.Port.value < 1 || raw.Port.value > 65535 {
		return invalidate(record, "port must be an integer from 1 through 65535.")
	}
	record.Port = uint16(raw.Port.value)

	transport, problem := parseTransport(raw.Transport)
	if problem != "" {
		return invalidate(record, problem)
	}
	record.Transport = transport
	security, problem := parseSecurity(raw.Security, record.Host)
	if problem != "" {
		return invalidate(record, problem)
	}
	record.Security = security
	return record
}

func advertisementIdentity(document []byte) Advertisement {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(document, &fields); err != nil {
		return Advertisement{}
	}
	var inboundTag string
	_ = json.Unmarshal(fields["inbound_tag"], &inboundTag)
	var name string
	_ = json.Unmarshal(fields["name"], &name)
	if name == "" {
		name = inboundTag
	}
	return Advertisement{InboundTag: inboundTag, Name: name}
}

func invalidAdvertisement(identity Advertisement, message string) Advertisement {
	return invalidate(identity, message)
}

func invalidate(record Advertisement, message string) Advertisement {
	record.validationError = &ValidationError{Message: message}
	return record
}

func parseTransport(document []byte) (Transport, string) {
	transportType, ok := objectType(document)
	if !ok {
		return Transport{}, "transport must be a typed object."
	}

	switch TransportType(transportType) {
	case TransportTCP:
		var raw struct {
			Type stringField `json:"type"`
		}
		if decodeStrict(document, &raw) != nil {
			return Transport{}, "tcp transport accepts only type."
		}
		return Transport{Type: TransportTCP}, ""
	case TransportWebSocket, TransportHTTPUpgrade:
		var raw httpTransportDocument
		if decodeStrict(document, &raw) != nil || !raw.Path.set || raw.Path.value == "" || !raw.Host.set || raw.Host.value == "" {
			return Transport{}, "WebSocket and HTTPUpgrade transport require non-empty path and host strings."
		}
		return Transport{Type: TransportType(transportType), Path: raw.Path.value, Host: raw.Host.value}, ""
	case TransportGRPC:
		var raw grpcTransportDocument
		if decodeStrict(document, &raw) != nil || !raw.ServiceName.set || raw.ServiceName.value == "" {
			return Transport{}, "gRPC transport requires a non-empty service_name string."
		}
		mode := raw.Mode.value
		if mode == "" {
			mode = "gun"
		}
		if mode != "gun" && mode != "multi" && mode != "guna" {
			return Transport{}, "gRPC mode must be gun, multi, or guna."
		}
		return Transport{
			Type:        TransportGRPC,
			ServiceName: raw.ServiceName.value,
			Mode:        mode,
			Authority:   raw.Authority.value,
		}, ""
	case TransportXHTTP:
		var raw xhttpTransportDocument
		if decodeStrict(document, &raw) != nil || !raw.Path.set || raw.Path.value == "" ||
			!raw.Host.set || raw.Host.value == "" || !raw.Mode.set || raw.Mode.value == "" {
			return Transport{}, "XHTTP transport requires non-empty path, host, and mode strings."
		}
		if raw.Mode.value != "auto" && raw.Mode.value != "packet-up" && raw.Mode.value != "stream-up" && raw.Mode.value != "stream-one" {
			return Transport{}, "XHTTP mode must be auto, packet-up, stream-up, or stream-one."
		}
		transport := Transport{
			Type: TransportXHTTP,
			Path: raw.Path.value,
			Host: raw.Host.value,
			Mode: raw.Mode.value,
		}
		if len(raw.Extra) != 0 {
			if !validJCSObject(raw.Extra) {
				return Transport{}, "XHTTP extra must be a JSON object accepted by JSON Canonicalization Scheme processing."
			}
			transport.extra = append([]byte(nil), raw.Extra...)
			transport.extraPresent = true
		}
		return transport, ""
	default:
		var raw struct {
			Type stringField `json:"type"`
		}
		if decodeStrict(document, &raw) != nil {
			return Transport{}, "unsupported transport accepts only type."
		}
		return Transport{Type: TransportType(transportType)}, ""
	}
}

func parseSecurity(document []byte, advertisedHost string) (Security, string) {
	securityType, ok := objectType(document)
	if !ok {
		return Security{}, "security must be a typed object."
	}

	switch SecurityType(securityType) {
	case SecurityTLS:
		var raw tlsSecurityDocument
		if decodeStrict(document, &raw) != nil {
			return Security{}, "TLS fields must use the documented names and JSON types."
		}
		fingerprint, problem := fingerprint(raw.Fingerprint)
		if problem != "" {
			return Security{}, problem
		}
		serverName := raw.ServerName.value
		if serverName == "" {
			serverName = advertisedHost
		}
		return Security{
			Type:            SecurityTLS,
			Fingerprint:     fingerprint,
			ServerName:      serverName,
			ECH:             raw.ECH.value,
			VerifyName:      raw.VerifyName.value,
			alpn:            append([]string(nil), raw.ALPN.values...),
			certificatePins: append([]string(nil), raw.CertificatePins.values...),
		}, ""
	case SecurityReality:
		var raw realitySecurityDocument
		if decodeStrict(document, &raw) != nil {
			return Security{}, "REALITY fields must use the documented names and JSON types."
		}
		fingerprint, problem := fingerprint(raw.Fingerprint)
		if problem != "" {
			return Security{}, problem
		}
		if !raw.ServerName.set || raw.ServerName.value == "" {
			return Security{}, "REALITY server_name is required and must not be empty."
		}
		if !raw.PublicKey.set || raw.PublicKey.value == "" {
			return Security{}, "REALITY public_key is required and must not be empty."
		}
		if !raw.ShortID.set {
			return Security{}, "REALITY short_id must be present."
		}
		return Security{
			Type:              SecurityReality,
			Fingerprint:       fingerprint,
			ServerName:        raw.ServerName.value,
			PublicKey:         raw.PublicKey.value,
			ShortID:           raw.ShortID.value,
			ShortIDPresent:    true,
			PostQuantumVerify: raw.PostQuantumVerify.value,
			SpiderX:           raw.SpiderX.value,
		}, ""
	case SecurityNone:
		var raw struct {
			Type stringField `json:"type"`
		}
		if decodeStrict(document, &raw) != nil {
			return Security{}, "none security accepts only type."
		}
		return Security{Type: SecurityNone}, ""
	default:
		var raw struct {
			Type stringField `json:"type"`
		}
		if decodeStrict(document, &raw) != nil {
			return Security{}, "unsupported security accepts only type."
		}
		return Security{Type: SecurityType(securityType)}, ""
	}
}

func validJCSObject(document []byte) bool {
	if !validUnicodeScalarEscapes(document) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false
	}
	return validJCSValue(object)
}

func validUnicodeScalarEscapes(document []byte) bool {
	for index := 0; index < len(document); {
		if document[index] != '"' {
			index++
			continue
		}
		index++
		for {
			if index >= len(document) {
				return false
			}
			if document[index] == '"' {
				index++
				break
			}
			if document[index] != '\\' {
				index++
				continue
			}
			index++
			if index >= len(document) {
				return false
			}
			if document[index] != 'u' {
				index++
				continue
			}
			if index+4 >= len(document) {
				return false
			}
			code, ok := parseHexCode(document[index+1 : index+5])
			if !ok {
				return false
			}
			index += 5
			switch {
			case code >= 0xD800 && code <= 0xDBFF:
				if index+5 >= len(document) || document[index] != '\\' || document[index+1] != 'u' {
					return false
				}
				low, ok := parseHexCode(document[index+2 : index+6])
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				index += 6
			case code >= 0xDC00 && code <= 0xDFFF:
				return false
			}
		}
	}
	return true
}

func parseHexCode(document []byte) (uint16, bool) {
	value, err := strconv.ParseUint(string(document), 16, 16)
	return uint16(value), err == nil
}

func validJCSValue(value any) bool {
	switch value := value.(type) {
	case nil, bool, string:
		return true
	case json.Number:
		number, err := strconv.ParseFloat(string(value), 64)
		return err == nil && !math.IsInf(number, 0) && !math.IsNaN(number)
	case []any:
		for _, item := range value {
			if !validJCSValue(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range value {
			if !validJCSValue(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func objectType(document []byte) (string, bool) {
	if len(document) == 0 {
		return "", false
	}
	var raw struct {
		Type stringField `json:"type"`
	}
	if err := json.Unmarshal(document, &raw); err != nil || !raw.Type.set || raw.Type.value == "" {
		return "", false
	}
	return raw.Type.value, true
}

func fingerprint(field stringField) (string, string) {
	if !field.set {
		return "chrome", ""
	}
	if field.value == "" {
		return "", "security fingerprint must not be empty when present."
	}
	return field.value, ""
}

func validAdvertisedHost(host string) bool {
	if host == "" || strings.HasSuffix(host, ".") || strings.ContainsAny(host, "/?#@%") ||
		strings.IndexFunc(host, unicode.IsSpace) >= 0 {
		return false
	}

	addressHost := host
	bracketed := strings.HasPrefix(host, "[")
	if bracketed {
		if !strings.HasSuffix(host, "]") {
			return false
		}
		addressHost = host[1 : len(host)-1]
	}
	if address, err := netip.ParseAddr(addressHost); err == nil {
		return address.Zone() == "" && (!bracketed || address.Is6())
	}
	if bracketed {
		return false
	}
	if strings.Contains(host, ":") {
		return false
	}

	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || len(ascii) > 253 || strings.HasSuffix(ascii, ".") {
		return false
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
	}
	numeric := true
	for _, char := range ascii {
		if char != '.' && (char < '0' || char > '9') {
			numeric = false
			break
		}
	}
	return !numeric
}

func markDuplicates(records []Advertisement) {
	counts := make(map[string]int)
	for _, record := range records {
		if record.InboundTag != "" {
			counts[record.InboundTag]++
		}
	}
	for index := range records {
		records[index].duplicate = records[index].InboundTag != "" && counts[records[index].InboundTag] > 1
	}
}

func validateJSONDocument(document []byte) error {
	if !utf8.Valid(document) {
		return errors.New("document is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("document contains trailing JSON values")
		}
		return err
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("duplicate object key %q", key)
			}
			keys[key] = struct{}{}
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("array is not terminated")
		}
	default:
		return errors.New("unexpected closing delimiter")
	}
	return nil
}

func decodeStrict(document []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("document contains trailing JSON values")
		}
		return err
	}
	return nil
}
