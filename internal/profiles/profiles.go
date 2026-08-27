// Package profiles evaluates and serializes read-only VLESS Connection profiles.
package profiles

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
	"github.com/xtls/xray-core/common/uuid"
	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/xrayconfig"
	"golang.org/x/net/idna"
)

// State describes whether profile candidates could be evaluated for one User.
type State string

const (
	StateReady             State = "ready"
	StateGoneUser          State = "gone_user"
	StateNoMatchingInbound State = "no_matching_inbound"
	StateSourceUnavailable State = "source_unavailable"
)

// Status distinguishes the two profile result variants.
type Status string

const (
	StatusAvailable   Status = "available"
	StatusUnavailable Status = "unavailable"
)

// Reason is a stable explanation for an unavailable candidate.
type Reason string

const (
	ReasonSourceUnavailable     Reason = "source_unavailable"
	ReasonAdvertisementMissing  Reason = "advertisement_missing"
	ReasonAdvertisementInvalid  Reason = "advertisement_invalid"
	ReasonDuplicateInboundTag   Reason = "duplicate_inbound_tag"
	ReasonDuplicateUser         Reason = "duplicate_user"
	ReasonInboundTagMissing     Reason = "inbound_tag_missing"
	ReasonReverseUser           Reason = "reverse_user"
	ReasonUnsupportedTransport  Reason = "unsupported_transport"
	ReasonUnsupportedSecurity   Reason = "unsupported_security"
	ReasonUnsupportedEncryption Reason = "unsupported_encryption"
	ReasonInsecureConnection    Reason = "insecure_connection"
	ReasonInboundMismatch       Reason = "inbound_mismatch"
	ReasonInvalidClientID       Reason = "invalid_client_id"
)

// Source identifies one input to profile evaluation.
type Source string

const (
	SourceXrayConfig     Source = "xray_config"
	SourceAdvertisements Source = "advertisements"
)

// SourceError is a safe current source failure in response order.
type SourceError struct {
	Source  Source
	Reason  string
	Message string
}

// Sources is the immutable source state needed for one evaluation. Callers
// may adapt watcher snapshots with SourcesFromSnapshots.
type Sources struct {
	XrayView      xrayconfig.View
	XrayAvailable bool
	XrayLoadedAt  time.Time
	XrayStale     bool
	XrayError     *xrayconfig.SourceError

	AdvertisementsView       advertisements.View
	AdvertisementsConfigured bool
	AdvertisementsAvailable  bool
	AdvertisementsLoadedAt   time.Time
	AdvertisementsStale      bool
	AdvertisementsError      *advertisements.SourceError
}

// SourcesFromSnapshots adapts the two watcher snapshots into evaluator input.
func SourcesFromSnapshots(xray xrayconfig.Snapshot, advertised advertisements.Snapshot) Sources {
	return Sources{
		XrayView: xray.View, XrayAvailable: xray.Available(), XrayLoadedAt: xray.LoadedAt,
		XrayStale: xray.Stale, XrayError: xray.Error,
		AdvertisementsView: advertised.View, AdvertisementsConfigured: advertised.Configured(),
		AdvertisementsAvailable: advertised.Available(), AdvertisementsLoadedAt: advertised.LoadedAt,
		AdvertisementsStale: advertised.Stale, AdvertisementsError: advertised.Error,
	}
}

// Collection is the ordered result for one exact User email.
type Collection struct {
	State    State
	LoadedAt *time.Time
	Stale    bool
	Errors   []SourceError
	Items    []Item
}

// Item contains exactly one result variant. Unavailable items cannot carry a
// Client ID, URI, or other partial credential.
type Item struct {
	Available   *Available
	Unavailable *Unavailable
}

// Status returns the result variant.
func (i Item) Status() Status {
	if i.Available != nil {
		return StatusAvailable
	}
	return StatusUnavailable
}

// Available is one complete client-ready Connection profile.
type Available struct {
	InboundTag string
	Name       string
	Topology   advertisements.Topology
	ClientID   string
	Flow       *string
	Endpoint   Endpoint
	Transport  Transport
	Security   Security
	URI        string
}

// Endpoint is the canonical public host and advertised port.
type Endpoint struct {
	Host string
	Port uint16
}

// Transport contains every typed advertised transport value used by the URI.
type Transport struct {
	Type        advertisements.TransportType
	Path        string
	Host        string
	ServiceName string
	Mode        string
	Authority   string
	Extra       json.RawMessage
}

// Security contains every typed advertised security value used by the URI.
type Security struct {
	Type              advertisements.SecurityType
	Fingerprint       string
	ServerName        string
	ALPN              []string
	ECH               string
	CertificatePins   []string
	VerifyName        string
	PublicKey         string
	ShortID           string
	ShortIDPresent    bool
	PostQuantumVerify string
	SpiderX           string
}

// Unavailable identifies one candidate and its stable failure without carrying
// any copyable credential.
type Unavailable struct {
	InboundTag *string
	Name       *string
	Reason     Reason
	Message    string
}

// Evaluate returns one result per matching VLESS inbound in xray config order.
func Evaluate(email string, gone bool, sources Sources) Collection {
	collection := sourceState(sources)
	if gone {
		collection.State = StateGoneUser
		collection.Items = []Item{}
		return collection
	}
	if !sources.XrayAvailable {
		collection.State = StateSourceUnavailable
		collection.Items = []Item{}
		return collection
	}

	inbounds := sources.XrayView.Inbounds()
	matching := make([]xrayconfig.Inbound, 0)
	tagCounts := make(map[string]int)
	for _, inbound := range inbounds {
		if inbound.Tag != "" {
			tagCounts[inbound.Tag]++
		}
		if !strings.EqualFold(inbound.Protocol, "vless") {
			continue
		}
		for _, user := range inbound.Users() {
			if user.Email == email {
				matching = append(matching, inbound)
				break
			}
		}
	}
	if len(matching) == 0 {
		collection.State = StateNoMatchingInbound
		collection.Items = []Item{}
		return collection
	}

	advertisementsByTag := make(map[string][]advertisements.Advertisement)
	if sources.AdvertisementsAvailable {
		for _, advertisement := range sources.AdvertisementsView.Advertisements() {
			advertisementsByTag[advertisement.InboundTag] = append(advertisementsByTag[advertisement.InboundTag], advertisement)
		}
	}

	items := make([]Item, 0, len(matching))
	for _, inbound := range matching {
		items = append(items, evaluateInbound(email, inbound, tagCounts[inbound.Tag], advertisementsByTag[inbound.Tag], sources))
	}
	collection.State = StateReady
	collection.Items = items
	return collection
}

func sourceState(sources Sources) Collection {
	collection := Collection{Errors: []SourceError{}}
	if sources.XrayAvailable && !sources.XrayLoadedAt.IsZero() {
		loadedAt := sources.XrayLoadedAt
		if sources.AdvertisementsAvailable && !sources.AdvertisementsLoadedAt.IsZero() && sources.AdvertisementsLoadedAt.Before(loadedAt) {
			loadedAt = sources.AdvertisementsLoadedAt
		}
		collection.LoadedAt = &loadedAt
	}
	collection.Stale = sources.XrayStale || sources.AdvertisementsStale
	if sources.XrayError != nil {
		collection.Errors = append(collection.Errors, SourceError{
			Source: SourceXrayConfig, Reason: string(sources.XrayError.Reason), Message: sources.XrayError.Message,
		})
	}
	if sources.AdvertisementsError != nil {
		collection.Errors = append(collection.Errors, SourceError{
			Source: SourceAdvertisements, Reason: string(sources.AdvertisementsError.Reason), Message: sources.AdvertisementsError.Message,
		})
	}
	return collection
}

func evaluateInbound(email string, inbound xrayconfig.Inbound, duplicateTagCount int, selected []advertisements.Advertisement, sources Sources) Item {
	users := matchingUsers(email, inbound.Users())
	var advertisement *advertisements.Advertisement
	if len(selected) == 1 {
		advertisement = &selected[0]
	}
	identityTag, identityName := unavailableIdentity(inbound, advertisement)

	if inbound.Tag == "" {
		return unavailable(nil, nil, ReasonInboundTagMissing, "The matching VLESS inbound has no tag.")
	}
	if duplicateTagCount > 1 || len(selected) > 1 {
		return unavailable(identityTag, duplicateAdvertisementName(selected), ReasonDuplicateInboundTag, "The inbound tag does not identify one unique inbound and advertisement.")
	}
	if len(users) > 1 {
		return unavailable(identityTag, identityName, ReasonDuplicateUser, "The User appears more than once in this inbound.")
	}
	user := users[0]
	if user.Reverse {
		return unavailable(identityTag, identityName, ReasonReverseUser, "The matching User is configured for reverse connections.")
	}
	if sources.AdvertisementsConfigured && !sources.AdvertisementsAvailable {
		return unavailable(identityTag, nil, ReasonSourceUnavailable, "The configured Advertised connection settings have not loaded successfully.")
	}
	if !sources.AdvertisementsConfigured || advertisement == nil {
		return unavailable(identityTag, nil, ReasonAdvertisementMissing, "No Advertised connection settings select this inbound.")
	}
	if problem := advertisement.ValidationError(); problem != nil {
		return unavailable(identityTag, identityName, ReasonAdvertisementInvalid, problem.Message)
	}
	if message := invalidClientValues(*advertisement); message != "" {
		return unavailable(identityTag, identityName, ReasonAdvertisementInvalid, message)
	}

	clientID, err := uuid.ParseString(user.ClientID)
	if err != nil {
		return unavailable(identityTag, identityName, ReasonInvalidClientID, "The configured Client ID cannot be converted to a UUID.")
	}

	advertisedTransportSupported := supportedTransport(advertisement.Transport.Type)
	inboundTransport, inboundTransportSupported := canonicalInboundTransport(inbound.Transport.Type)
	if !advertisedTransportSupported || (advertisement.Topology == advertisements.TopologyDirect && (!inboundTransportSupported || unsupportedDirectTransportFeature(inbound))) {
		return unavailable(identityTag, identityName, ReasonUnsupportedTransport, "The applicable transport is not supported for Connection profiles.")
	}
	advertisedSecuritySupported := supportedSecurity(advertisement.Security.Type)
	inboundSecurity, inboundSecuritySupported := canonicalInboundSecurity(inbound.Security.Type)
	if !advertisedSecuritySupported || (advertisement.Topology == advertisements.TopologyDirect && !inboundSecuritySupported) {
		return unavailable(identityTag, identityName, ReasonUnsupportedSecurity, "The applicable transport security is not supported for Connection profiles.")
	}
	if !strings.EqualFold(defaultNone(inbound.Decryption), "none") {
		return unavailable(identityTag, identityName, ReasonUnsupportedEncryption, "The inbound requires unsupported VLESS Encryption.")
	}
	if advertisement.Security.Type == advertisements.SecurityNone {
		return unavailable(identityTag, identityName, ReasonInsecureConnection, "An unsecured VLESS profile is not available.")
	}

	flow := user.Flow
	if flow == "" {
		flow = inbound.Flow
	}
	if !compatibleShape(flow, advertisement.Transport.Type, advertisement.Security.Type) ||
		!emptyRealityShortIDAccepted(inbound, *advertisement) ||
		(advertisement.Topology == advertisements.TopologyDirect && !directMatch(inbound, inboundTransport, inboundSecurity, *advertisement)) {
		return unavailable(identityTag, identityName, ReasonInboundMismatch, "The advertised client view is incompatible with the matching inbound or effective flow.")
	}

	profile, err := buildAvailable(email, clientID.String(), flow, *advertisement)
	if err != nil {
		return unavailable(identityTag, identityName, ReasonAdvertisementInvalid, "The Advertised connection settings cannot be serialized canonically.")
	}
	return Item{Available: &profile}
}

func matchingUsers(email string, users []xrayconfig.InboundUser) []xrayconfig.InboundUser {
	return slices.DeleteFunc(users, func(user xrayconfig.InboundUser) bool { return user.Email != email })
}

func unavailableIdentity(inbound xrayconfig.Inbound, advertisement *advertisements.Advertisement) (*string, *string) {
	var tag *string
	if inbound.Tag != "" {
		tag = stringPointer(inbound.Tag)
	}
	if advertisement == nil || advertisement.Name == "" {
		return tag, nil
	}
	return tag, stringPointer(advertisement.Name)
}

func duplicateAdvertisementName(selected []advertisements.Advertisement) *string {
	if len(selected) != 1 || selected[0].Name == "" {
		return nil
	}
	return stringPointer(selected[0].Name)
}

func unavailable(tag, name *string, reason Reason, message string) Item {
	return Item{Unavailable: &Unavailable{InboundTag: tag, Name: name, Reason: reason, Message: message}}
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}

func defaultNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func supportedTransport(transport advertisements.TransportType) bool {
	switch transport {
	case advertisements.TransportTCP, advertisements.TransportWebSocket, advertisements.TransportHTTPUpgrade,
		advertisements.TransportGRPC, advertisements.TransportXHTTP:
		return true
	default:
		return false
	}
}

func canonicalInboundTransport(value string) (advertisements.TransportType, bool) {
	switch strings.ToLower(value) {
	case "", "raw", "tcp":
		return advertisements.TransportTCP, true
	case "ws", "websocket":
		return advertisements.TransportWebSocket, true
	case "httpupgrade":
		return advertisements.TransportHTTPUpgrade, true
	case "grpc":
		return advertisements.TransportGRPC, true
	case "xhttp", "splithttp":
		return advertisements.TransportXHTTP, true
	default:
		return "", false
	}
}

func supportedSecurity(security advertisements.SecurityType) bool {
	switch security {
	case advertisements.SecurityTLS, advertisements.SecurityReality, advertisements.SecurityNone:
		return true
	default:
		return false
	}
}

func canonicalInboundSecurity(value string) (advertisements.SecurityType, bool) {
	switch strings.ToLower(value) {
	case "", "none":
		return advertisements.SecurityNone, true
	case "tls":
		return advertisements.SecurityTLS, true
	case "reality":
		return advertisements.SecurityReality, true
	default:
		return "", false
	}
}

func unsupportedDirectTransportFeature(inbound xrayconfig.Inbound) bool {
	if inbound.Transport.FinalMaskConfigured {
		return true
	}
	transport, supported := canonicalInboundTransport(inbound.Transport.Type)
	return supported && transport == advertisements.TransportTCP && inbound.Transport.RawHeaderConfigured &&
		!strings.EqualFold(defaultNone(inbound.Transport.RawHeaderType), "none")
}

func emptyRealityShortIDAccepted(inbound xrayconfig.Inbound, advertisement advertisements.Advertisement) bool {
	return advertisement.Security.Type != advertisements.SecurityReality || advertisement.Security.ShortID != "" ||
		containsFold(inbound.Security.Reality.ShortIDs(), "")
}

func compatibleShape(flow string, transport advertisements.TransportType, security advertisements.SecurityType) bool {
	if flow != "" && flow != "xtls-rprx-vision" {
		return false
	}
	if security == advertisements.SecurityReality && transport != advertisements.TransportTCP &&
		transport != advertisements.TransportGRPC && transport != advertisements.TransportXHTTP {
		return false
	}
	if flow == "xtls-rprx-vision" {
		return transport == advertisements.TransportTCP &&
			(security == advertisements.SecurityTLS || security == advertisements.SecurityReality)
	}
	return true
}

func directMatch(inbound xrayconfig.Inbound, inboundTransport advertisements.TransportType, inboundSecurity advertisements.SecurityType, advertisement advertisements.Advertisement) bool {
	if inboundTransport != advertisement.Transport.Type || inboundSecurity != advertisement.Security.Type {
		return false
	}

	switch inboundTransport {
	case advertisements.TransportTCP:
	case advertisements.TransportWebSocket:
		if normalizedPath(inbound.Transport.WebSocket.Path) != advertisement.Transport.Path ||
			!acceptedHost(inbound.Transport.WebSocket.Host, advertisement.Transport.Host) {
			return false
		}
	case advertisements.TransportHTTPUpgrade:
		if normalizedPath(inbound.Transport.HTTPUpgrade.Path) != advertisement.Transport.Path ||
			!acceptedHost(inbound.Transport.HTTPUpgrade.Host, advertisement.Transport.Host) {
			return false
		}
	case advertisements.TransportGRPC:
		if !acceptedGRPCService(inbound.Transport.GRPC.ServiceName, advertisement.Transport.ServiceName, advertisement.Transport.Mode) {
			return false
		}
	case advertisements.TransportXHTTP:
		if normalizedPath(inbound.Transport.XHTTP.Path) != advertisement.Transport.Path ||
			!acceptedHost(inbound.Transport.XHTTP.Host, advertisement.Transport.Host) ||
			normalizedXHTTPMode(inbound.Transport.XHTTP.Mode) != advertisement.Transport.Mode {
			return false
		}
	}

	switch inboundSecurity {
	case advertisements.SecurityTLS:
		if inbound.Security.TLS.RejectUnknownSNI && inbound.Security.TLS.ServerName != "" &&
			!equalHost(inbound.Security.TLS.ServerName, advertisement.Security.ServerName) {
			return false
		}
	case advertisements.SecurityReality:
		if !containsHost(inbound.Security.Reality.ServerNames(), advertisement.Security.ServerName) ||
			!containsFold(inbound.Security.Reality.ShortIDs(), advertisement.Security.ShortID) {
			return false
		}
	}
	return true
}

func normalizedPath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func normalizedXHTTPMode(mode string) string {
	if mode == "" {
		return "auto"
	}
	return mode
}

func acceptedHost(inboundHost, advertisedHost string) bool {
	return inboundHost == "" || equalHost(inboundHost, advertisedHost)
}

func acceptedGRPCService(inbound, advertised, mode string) bool {
	if !strings.HasPrefix(inbound, "/") {
		return inbound == advertised
	}
	serverPrefix, serverNames, ok := splitGRPCService(inbound)
	if !ok || len(serverNames) == 0 || strings.Contains(advertised, "|") {
		return false
	}
	clientPrefix, clientNames, ok := splitGRPCService(advertised)
	if !ok || len(clientNames) != 1 || clientPrefix != serverPrefix {
		return false
	}
	wanted := serverNames[0]
	if mode == "multi" && len(serverNames) > 1 {
		wanted = serverNames[1]
	}
	return clientNames[0] == wanted
}

func splitGRPCService(service string) (string, []string, bool) {
	lastSlash := strings.LastIndex(service, "/")
	if !strings.HasPrefix(service, "/") || lastSlash < 1 || lastSlash == len(service)-1 {
		return "", nil, false
	}
	return service[:lastSlash], strings.Split(service[lastSlash+1:], "|"), true
}

func containsHost(hosts []string, wanted string) bool {
	for _, host := range hosts {
		if equalHost(host, wanted) {
			return true
		}
	}
	return false
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func equalHost(left, right string) bool {
	leftCanonical, leftOK := canonicalHost(left)
	rightCanonical, rightOK := canonicalHost(right)
	if leftOK && rightOK {
		return subtle.ConstantTimeCompare([]byte(leftCanonical), []byte(rightCanonical)) == 1
	}
	return strings.EqualFold(left, right)
}

func invalidClientValues(advertisement advertisements.Advertisement) string {
	if _, ok := canonicalHost(advertisement.Host); !ok {
		return "The advertised public host cannot be canonicalized."
	}
	security := advertisement.Security
	if security.Type != advertisements.SecurityTLS && security.Type != advertisements.SecurityReality {
		return ""
	}
	if _, ok := canonicalHost(security.ServerName); !ok {
		return "The advertised security server_name is not a valid host."
	}
	if security.Type == advertisements.SecurityTLS && security.VerifyName != "" {
		if _, ok := canonicalHost(security.VerifyName); !ok {
			return "The advertised TLS verify_name is not a valid host."
		}
	}
	if security.Type != advertisements.SecurityReality {
		return ""
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(security.PublicKey)
	if err != nil || len(publicKey) != 32 {
		return "The advertised REALITY public_key is not a valid X25519 public key."
	}
	if len(security.ShortID) > 16 {
		return "The advertised REALITY short_id is not valid hexadecimal data."
	}
	if _, err := hex.DecodeString(security.ShortID); err != nil {
		return "The advertised REALITY short_id is not valid hexadecimal data."
	}
	if security.PostQuantumVerify != "" {
		verify, err := base64.RawURLEncoding.DecodeString(security.PostQuantumVerify)
		if err != nil || len(verify) != 1952 {
			return "The advertised REALITY post_quantum_verify value is invalid."
		}
	}
	if security.SpiderX != "" && !strings.HasPrefix(security.SpiderX, "/") {
		return "The advertised REALITY spider_x must start with a slash."
	}
	return ""
}

func canonicalHost(host string) (string, bool) {
	if host == "" || strings.HasSuffix(host, ".") {
		return "", false
	}
	addressHost := host
	bracketed := strings.HasPrefix(host, "[")
	if bracketed {
		if !strings.HasSuffix(host, "]") {
			return "", false
		}
		addressHost = host[1 : len(host)-1]
	}
	if address, err := netip.ParseAddr(addressHost); err == nil {
		if address.Zone() != "" || (bracketed && !address.Is6()) {
			return "", false
		}
		if address.Is6() {
			return "[" + address.String() + "]", true
		}
		return address.String(), true
	}
	if bracketed || strings.ContainsAny(host, "[]:/?#@%") {
		return "", false
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || len(ascii) > 253 || strings.HasSuffix(ascii, ".") {
		return "", false
	}
	numeric := true
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 {
			return "", false
		}
		for _, char := range label {
			if char < '0' || char > '9' {
				numeric = false
			}
		}
	}
	if numeric {
		return "", false
	}
	return strings.ToLower(ascii), true
}

func buildAvailable(email, clientID, flow string, advertisement advertisements.Advertisement) (Available, error) {
	host, ok := canonicalHost(advertisement.Host)
	if !ok {
		return Available{}, strconv.ErrSyntax
	}
	transport := Transport{
		Type:        advertisement.Transport.Type,
		Path:        advertisement.Transport.Path,
		Host:        advertisement.Transport.Host,
		ServiceName: advertisement.Transport.ServiceName,
		Mode:        advertisement.Transport.Mode,
		Authority:   advertisement.Transport.Authority,
	}
	if extra, present := advertisement.Transport.Extra(); present {
		canonical, err := jcs.Transform(extra)
		if err != nil {
			return Available{}, err
		}
		transport.Extra = json.RawMessage(canonical)
	}
	serverName, ok := canonicalHost(advertisement.Security.ServerName)
	if !ok {
		return Available{}, strconv.ErrSyntax
	}
	verifyName := ""
	if advertisement.Security.VerifyName != "" {
		verifyName, ok = canonicalHost(advertisement.Security.VerifyName)
		if !ok {
			return Available{}, strconv.ErrSyntax
		}
	}
	security := Security{
		Type:              advertisement.Security.Type,
		Fingerprint:       advertisement.Security.Fingerprint,
		ServerName:        serverName,
		ALPN:              advertisement.Security.ALPN(),
		ECH:               advertisement.Security.ECH,
		CertificatePins:   advertisement.Security.CertificatePins(),
		VerifyName:        verifyName,
		PublicKey:         advertisement.Security.PublicKey,
		ShortID:           advertisement.Security.ShortID,
		ShortIDPresent:    advertisement.Security.ShortIDPresent,
		PostQuantumVerify: advertisement.Security.PostQuantumVerify,
		SpiderX:           advertisement.Security.SpiderX,
	}
	profile := Available{
		InboundTag: advertisement.InboundTag,
		Name:       advertisement.Name,
		Topology:   advertisement.Topology,
		ClientID:   clientID,
		Endpoint:   Endpoint{Host: host, Port: advertisement.Port},
		Transport:  transport,
		Security:   security,
	}
	if flow != "" {
		profile.Flow = stringPointer(flow)
	}
	profile.URI = serializeURI(email, profile)
	return profile, nil
}
