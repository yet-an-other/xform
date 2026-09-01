package xrayconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// NewClient is one roster client the render adds to a managed inbound's
// clients array. An empty Flow is omitted from the rendered object.
type NewClient struct {
	Email string
	ID    string
	Flow  string
}

// RenderClients renders roster additions into an xray config document via
// raw-span surgery (user-management spec §4): a token scan locates each
// managed inbound's settings.clients array and only those spans change.
// Key order, unknown fields, and JSONC comments elsewhere stay byte-stable;
// existing clients keep their positions and new clients append at the end.
//
// Render is additive and store-respecting: a client the array already
// carries — by email, the identity — is never rewritten, so a hand-edited
// credential survives and retries are byte-identical no-ops. A managed tag
// the document does not have is skipped (the live push reports the drift);
// one naming a non-VLESS inbound is an error, because the panel manages
// VLESS only. changed reports whether rendered differs from document.
func RenderClients(document []byte, adds map[string][]NewClient) (rendered []byte, changed bool, err error) {
	if len(adds) == 0 {
		return document, false, nil
	}
	root, err := parseJSONC(document)
	if err != nil {
		return nil, false, fmt.Errorf("scan xray config: %w", err)
	}
	inbounds := root.get("inbounds")
	if inbounds == nil || inbounds.kind != '[' {
		return nil, false, fmt.Errorf("scan xray config: no inbounds array")
	}

	// Insertions compose because every one is a pure insert: applied from
	// last to first, earlier offsets stay valid.
	var inserts []insertion
	for _, inbound := range inbounds.items {
		if inbound.kind != '{' {
			continue
		}
		tag := inbound.get("tag")
		if tag == nil || tag.kind != '"' {
			continue
		}
		add, managed := adds[tag.value]
		if !managed {
			continue
		}
		protocol := inbound.get("protocol")
		if protocol == nil || !strings.EqualFold(protocol.value, "vless") {
			return nil, false, fmt.Errorf("render clients: inbound %q is not VLESS", tag.value)
		}
		settings := inbound.get("settings")
		if settings == nil || settings.kind != '{' {
			return nil, false, fmt.Errorf("render clients: inbound %q has no settings object", tag.value)
		}
		insert, err := planClientsInsert(document, settings, add)
		if err != nil {
			return nil, false, err
		}
		if insert != nil {
			inserts = append(inserts, *insert)
		}
	}
	if len(inserts) == 0 {
		return document, false, nil
	}

	slices.SortFunc(inserts, func(a, b insertion) int { return b.at - a.at })
	rendered = slices.Clone(document)
	for _, insert := range inserts {
		rendered = slices.Insert(rendered, insert.at, []byte(insert.text)...)
	}
	return rendered, true, nil
}

// insertion is one pure text insert at a byte offset.
type insertion struct {
	at   int
	text string
}

// planClientsInsert plans one managed inbound's append: the clients array
// gains every add its emails do not already carry, or — when settings has no
// clients key yet — the key itself is inserted. Nil when nothing remains to
// add.
func planClientsInsert(document []byte, settings *node, add []NewClient) (*insertion, error) {
	clients := settings.get("clients")
	if clients == nil {
		// The ansible template no longer renders clients lists: the key is
		// inserted inside the settings object, at its own indent when the
		// object spans lines and inline when it does not.
		encodedKey, _ := json.Marshal("clients")
		key := string(encodedKey)
		if len(settings.items) == 0 {
			if multiline(document, settings.start, settings.end) {
				indent := lineIndent(document, settings.start) + "  "
				text := "\n" + indent + key + ": " + renderClientsValue(indent, add) + "\n" + lineIndent(document, settings.start)
				return &insertion{at: settings.start + 1, text: text}, nil
			}
			return &insertion{at: settings.start + 1, text: key + ": [" + strings.Join(renderEntries(add), ", ") + "]"}, nil
		}
		last := settings.items[len(settings.items)-1]
		if multiline(document, settings.start, settings.end) {
			indent := lineIndent(document, settings.propStarts[len(settings.propStarts)-1])
			return &insertion{at: last.end, text: ",\n" + indent + key + ": " + renderClientsValue(indent, add)}, nil
		}
		return &insertion{at: last.end, text: ", " + key + ": [" + strings.Join(renderEntries(add), ", ") + "]"}, nil
	}
	if clients.kind != '[' {
		return nil, fmt.Errorf("render clients: clients is not an array")
	}

	existing := map[string]bool{}
	for _, entry := range clients.items {
		if email := entry.get("email"); email != nil && email.kind == '"' {
			existing[email.value] = true
		}
	}
	remaining := make([]NewClient, 0, len(add))
	for _, client := range add {
		if !existing[client.Email] {
			remaining = append(remaining, client)
		}
	}
	if len(remaining) == 0 {
		return nil, nil
	}

	entries := renderEntries(remaining)
	if len(clients.items) == 0 {
		// An empty array takes the entries between its brackets: inline when
		// the array is written inline, on fresh lines otherwise.
		if multiline(document, clients.start, clients.end) {
			keyIndent := lineIndent(document, clients.start)
			text := "\n" + keyIndent + "  " + strings.Join(entries, ",\n"+keyIndent+"  ") + "\n" + keyIndent
			return &insertion{at: clients.start + 1, text: text}, nil
		}
		return &insertion{at: clients.start + 1, text: strings.Join(entries, ", ")}, nil
	}

	last := clients.items[len(clients.items)-1]
	if clients.trailing {
		// The comma after the last element becomes the separator: the new
		// entries follow it, so no comma is added and the array gains no
		// dangling comma the panel's strict parser would reject. Multiline
		// arrays insert at the closing bracket's line start, keeping any
		// trailing comment with the element it annotates.
		if multiline(document, clients.start, clients.end) {
			closer := bytes.LastIndexByte(document[:clients.end-1], '\n') + 1
			indent := lineIndent(document, last.start)
			return &insertion{at: closer, text: indent + strings.Join(entries, ",\n"+indent) + "\n"}, nil
		}
		comma := bytes.IndexByte(document[last.end:clients.end], ',')
		return &insertion{at: last.end + comma + 1, text: " " + strings.Join(entries, ", ")}, nil
	}
	if multiline(document, clients.start, clients.end) {
		return &insertion{at: last.end, text: ",\n" + lineIndent(document, last.start) + strings.Join(entries, ",\n"+lineIndent(document, last.start))}, nil
	}
	return &insertion{at: last.end, text: ", " + strings.Join(entries, ", ")}, nil
}

// renderClientsValue renders a whole clients array for an inbound that had
// no clients key yet, one compact object per line at the key's indent.
func renderClientsValue(keyIndent string, add []NewClient) string {
	entries := renderEntries(add)
	return "[\n" + keyIndent + "  " + strings.Join(entries, ",\n"+keyIndent+"  ") + "\n" + keyIndent + "]"
}

// renderEntries marshals the additions as compact one-line objects — email,
// id, then flow when set — in the hand-written style of the configs the
// panel manages (a space after each colon).
func renderEntries(add []NewClient) []string {
	entries := make([]string, 0, len(add))
	for _, client := range add {
		email, _ := json.Marshal(client.Email) // strings marshal cleanly
		id, _ := json.Marshal(client.ID)
		entry := `{"email": ` + string(email) + `, "id": ` + string(id)
		if client.Flow != "" {
			flow, _ := json.Marshal(client.Flow)
			entry += `, "flow": ` + string(flow)
		}
		entries = append(entries, entry+"}")
	}
	return entries
}

// multiline reports whether the span contains a line break.
func multiline(document []byte, start, end int) bool {
	return bytes.Contains(document[start:end], []byte("\n"))
}

// lineIndent returns the whitespace that begins the line containing pos.
func lineIndent(document []byte, pos int) string {
	line := bytes.LastIndexByte(document[:pos], '\n') + 1
	end := line
	for end < len(document) && (document[end] == ' ' || document[end] == '\t') {
		end++
	}
	return string(document[line:end])
}

// node is one parsed JSONC value with its byte span. Objects carry their
// keys and values; arrays their elements; strings their decoded form.
// trailing marks a comma directly before the closer (JSONC, not JSON).
type node struct {
	kind       byte // '{', '[', '"', or 'l' for a literal
	start, end int  // end is exclusive, right after the closing token
	value      string
	keys       []string
	propStarts []int // byte offset of each key, for indentation
	items      []*node
	trailing   bool
}

// get returns an object's value for key, or nil.
func (n *node) get(key string) *node {
	if n.kind != '{' {
		return nil
	}
	for i, k := range n.keys {
		if k == key {
			return n.items[i]
		}
	}
	return nil
}

// parseJSONC parses one JSONC document — comments and trailing commas
// tolerated — into a span-carrying tree. The render needs positions, not a
// typed value; xray's own loader accepts this grammar.
func parseJSONC(document []byte) (*node, error) {
	parser := &jsoncParser{src: document}
	root, err := parser.value()
	if err != nil {
		return nil, err
	}
	parser.blank()
	if parser.pos != len(document) {
		return nil, fmt.Errorf("trailing content at byte %d", parser.pos)
	}
	return root, nil
}

type jsoncParser struct {
	src []byte
	pos int
}

// blank skips whitespace and both comment forms.
func (p *jsoncParser) blank() {
	for p.pos < len(p.src) {
		switch c := p.src[p.pos]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.pos++
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			p.pos += 2
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			p.pos += 2
			for p.pos+1 < len(p.src) && !(p.src[p.pos] == '*' && p.src[p.pos+1] == '/') {
				p.pos++
			}
			p.pos += 2 // unterminated comments end at EOF; the parser errors there
		default:
			return
		}
	}
}

func (p *jsoncParser) value() (*node, error) {
	p.blank()
	if p.pos >= len(p.src) {
		return nil, fmt.Errorf("unexpected end of document")
	}
	switch p.src[p.pos] {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"':
		return p.str()
	default:
		return p.literal()
	}
}

func (p *jsoncParser) object() (*node, error) {
	object := &node{kind: '{', start: p.pos}
	p.pos++ // consume '{'
	for {
		p.blank()
		if p.pos >= len(p.src) {
			return nil, fmt.Errorf("unterminated object at byte %d", object.start)
		}
		if p.src[p.pos] == '}' {
			p.pos++
			object.end = p.pos
			return object, nil
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		p.blank()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, fmt.Errorf("missing ':' after object key at byte %d", key.end)
		}
		p.pos++
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		object.keys = append(object.keys, key.value)
		object.propStarts = append(object.propStarts, key.start)
		object.items = append(object.items, value)
		p.blank()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
			p.blank()
			if p.pos < len(p.src) && p.src[p.pos] == '}' {
				object.trailing = true
			}
		}
	}
}

func (p *jsoncParser) array() (*node, error) {
	array := &node{kind: '[', start: p.pos}
	p.pos++ // consume '['
	for {
		p.blank()
		if p.pos >= len(p.src) {
			return nil, fmt.Errorf("unterminated array at byte %d", array.start)
		}
		if p.src[p.pos] == ']' {
			p.pos++
			array.end = p.pos
			return array, nil
		}
		item, err := p.value()
		if err != nil {
			return nil, err
		}
		array.items = append(array.items, item)
		p.blank()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
			p.blank()
			if p.pos < len(p.src) && p.src[p.pos] == ']' {
				array.trailing = true
			}
		}
	}
}

// str parses one string, decoding its value for key and email comparison.
func (p *jsoncParser) str() (*node, error) {
	start := p.pos
	p.pos++ // consume the opening quote
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '\\':
			p.pos += 2
		case '"':
			p.pos++
			str := &node{kind: '"', start: start, end: p.pos}
			if err := json.Unmarshal(p.src[start:p.pos], &str.value); err != nil {
				return nil, fmt.Errorf("malformed string at byte %d: %w", start, err)
			}
			return str, nil
		default:
			p.pos++
		}
	}
	return nil, fmt.Errorf("unterminated string at byte %d", start)
}

// literal scans a number, true, false, or null — values the render never
// inspects, so only the span matters.
func (p *jsoncParser) literal() (*node, error) {
	start := p.pos
	for p.pos < len(p.src) {
		switch c := p.src[p.pos]; c {
		case ' ', '\t', '\r', '\n', ',', '}', ']', ':':
			return &node{kind: 'l', start: start, end: p.pos}, nil
		case '/':
			if p.pos+1 < len(p.src) && (p.src[p.pos+1] == '/' || p.src[p.pos+1] == '*') {
				return &node{kind: 'l', start: start, end: p.pos}, nil
			}
			p.pos++
		default:
			p.pos++
		}
	}
	return &node{kind: 'l', start: start, end: p.pos}, nil
}
