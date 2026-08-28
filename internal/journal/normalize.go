package journal

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// unitFields are the trusted fields journald attaches, in the precedence
// §6.4 fixes. A client cannot forge them, which is why the `unit` column
// reads from these and never from a user field.
var unitFields = []string{"_SYSTEMD_UNIT", "UNIT", "OBJECT_SYSTEMD_UNIT", "COREDUMP_UNIT"}

const (
	encodingUTF8   = "utf-8"
	encodingBase64 = "base64"
)

// normalize turns one journalctl JSON object into an Entry, or reports that
// the whole snapshot is malformed. Individual records are never skipped: a
// snapshot missing entries it did not mention would be a lie about what the
// journal holds (§6.4).
func normalize(record map[string]json.RawMessage, fallbackUnit string) (Entry, error) {
	cursor, ok := scalarString(record["__CURSOR"])
	if !ok || cursor == "" {
		return Entry{}, errors.New("no usable __CURSOR")
	}
	timestamp, err := timestampMicroseconds(record["__REALTIME_TIMESTAMP"])
	if err != nil {
		return Entry{}, err
	}
	unit, err := unitOf(record, fallbackUnit)
	if err != nil {
		return Entry{}, err
	}
	message, err := messageOf(record)
	if err != nil {
		return Entry{}, err
	}

	entry := Entry{
		Cursor:      cursor,
		TimestampUS: timestamp,
		Unit:        unit,
		Identifier:  optionalString(record["SYSLOG_IDENTIFIER"]),
		PID:         optionalNumber(record["_PID"], 0, math.MaxInt),
		Priority:    optionalNumber(record["PRIORITY"], 0, 7),
	}
	entry.Message, entry.MessageEncoding, entry.MessageTruncated = message.text, message.encoding, message.truncated
	return entry, nil
}

// unitOf takes the first non-empty trusted unit field, falling back to the
// unit the snapshot was collected for.
func unitOf(record map[string]json.RawMessage, fallbackUnit string) (string, error) {
	for _, field := range unitFields {
		raw, present := record[field]
		// §6.4 makes an array, object, or number malformed; null is none of
		// those, and journald writes it for a field it elided, so it reads as
		// absent and the next candidate is tried.
		if !present || isNull(raw) {
			continue
		}
		value, ok := scalarString(raw)
		if !ok {
			// A repeated or structured trusted field means this is not the
			// journalctl output shape the contract was written against.
			return "", fmt.Errorf("trusted field %s is not a scalar string", field)
		}
		if value != "" {
			return value, nil
		}
	}
	return fallbackUnit, nil
}

func timestampMicroseconds(raw json.RawMessage) (uint64, error) {
	value, ok := scalarString(raw)
	if !ok || !isDecimalDigits(value) {
		return 0, errors.New("no usable __REALTIME_TIMESTAMP")
	}
	microseconds, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, errors.New("__REALTIME_TIMESTAMP does not fit an unsigned 64-bit value")
	}
	return microseconds, nil
}

// message is the normalized MESSAGE field in its three reportable shapes:
// text, binary, or elided by journalctl for exceeding its field limit.
type message struct {
	text      *string
	encoding  *string
	truncated bool
}

func messageOf(record map[string]json.RawMessage) (message, error) {
	raw, present := record["MESSAGE"]
	if !present {
		// A record with no MESSAGE is an empty message, not a broken one.
		return textMessage(""), nil
	}
	if isNull(raw) {
		// journalctl replaces a field past its size limit with null unless
		// --all is passed, which the reader deliberately never does.
		return message{truncated: true}, nil
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return message{}, errors.New("MESSAGE is not valid JSON")
	}
	switch typed := value.(type) {
	case string:
		return textMessage(typed), nil
	case []any:
		return repeatedMessage(typed)
	default:
		return message{}, errors.New("MESSAGE is neither a string, an array, nor null")
	}
}

// repeatedMessage handles journalctl's two array forms: a field repeated
// within one entry, and non-UTF-8 data rendered as a byte array.
func repeatedMessage(values []any) (message, error) {
	if len(values) == 0 {
		return message{}, errors.New("MESSAGE is an empty array")
	}
	switch values[0].(type) {
	case string:
		joined := make([]byte, 0, len(values)*32)
		for index, value := range values {
			text, ok := value.(string)
			if !ok {
				return message{}, errors.New("MESSAGE mixes strings with other values")
			}
			if index > 0 {
				joined = append(joined, '\n')
			}
			joined = append(joined, text...)
		}
		return textMessage(string(joined)), nil
	case float64:
		bytes := make([]byte, 0, len(values))
		for _, value := range values {
			number, ok := value.(float64)
			if !ok {
				return message{}, errors.New("MESSAGE mixes bytes with other values")
			}
			if number != math.Trunc(number) || number < 0 || number > 255 {
				return message{}, errors.New("MESSAGE contains a value outside a byte range")
			}
			bytes = append(bytes, byte(number))
		}
		encoded := base64.StdEncoding.EncodeToString(bytes)
		encoding := encodingBase64
		return message{text: &encoded, encoding: &encoding}, nil
	default:
		return message{}, errors.New("MESSAGE is an array of neither strings nor bytes")
	}
}

func textMessage(text string) message {
	encoding := encodingUTF8
	return message{text: &text, encoding: &encoding}
}

// optionalString returns a scalar string field, or nil for anything else —
// absent, null, repeated, or structured. These are untrusted client fields,
// so an odd shape normalizes away rather than failing the snapshot.
func optionalString(raw json.RawMessage) *string {
	value, ok := scalarString(raw)
	if !ok {
		return nil
	}
	return &value
}

// optionalNumber parses a decimal string field within an inclusive range,
// normalizing anything else to nil.
func optionalNumber(raw json.RawMessage, low, high int) *int {
	value, ok := scalarString(raw)
	if !ok || !isDecimalDigits(value) {
		return nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < low || number > high {
		return nil
	}
	return &number
}

func scalarString(raw json.RawMessage) (string, bool) {
	// Absent and null are both "no value": unmarshalling null into a string
	// succeeds and leaves it empty, which would otherwise pass for one.
	if len(raw) == 0 || isNull(raw) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func isNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// isDecimalDigits rejects the signs and spaces Go's parsers would otherwise
// accept, so only journald's own decimal form is taken.
func isDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
