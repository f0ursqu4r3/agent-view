package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// member is one key of a JSON object with its value bytes as received.
type member struct {
	key string
	val json.RawMessage
}

// envelopeKeys are the keys of an envelope that a Raw event does not keep in
// its Fields: the encoder writes them itself.
var envelopeKeys = map[string]bool{"v": true, "host": true, "seq": true, "kind": true, "ts": true}

// scanObject splits one JSON object into its members, in the order received.
// It fails when the line is not exactly one JSON object.
func scanObject(line []byte) ([]member, error) {
	if !json.Valid(line) {
		return nil, errors.New("wire: line is not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("wire: line is not a JSON object")
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out = append(out, member{key, val})
	}
	return out, nil
}

// find returns the value of the last member with the key, as JavaScript does
// for a repeated key.
func find(ms []member, key string) (json.RawMessage, bool) {
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].key == key {
			return ms[i].val, true
		}
	}
	return nil, false
}

func stringField(ms []member, key string) (string, error) {
	raw, ok := find(ms, key)
	if !ok {
		return "", fmt.Errorf("wire: missing %q", key)
	}
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", fmt.Errorf("wire: %q is not a string", key)
	}
	return s, nil
}

// maxSafeInt is the largest integer that a JavaScript number holds exactly.
const maxSafeInt = 1 << 53

func intField(ms []member, key string) (int64, error) {
	raw, ok := find(ms, key)
	if !ok {
		return 0, fmt.Errorf("wire: missing %q", key)
	}
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, fmt.Errorf("wire: %q is not a number", key)
	}
	if n, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > maxSafeInt {
		return 0, fmt.Errorf("wire: %q is not an integer", key)
	}
	return int64(f), nil
}

// decodeAs decodes the line into a new T. Keys that T does not define are
// dropped, including the envelope keys.
func decodeAs[T Event](line []byte) (Event, error) {
	var ev T
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// requiredObject names the member that a kind cannot do without. The state
// cannot apply such an event when the member is missing or null (TypeScript's
// apply throws), so the line is an error.
var requiredObject = map[string]string{"agent.upsert": "agent", "tool.start": "call", "coverage": "coverage"}

// decodeEvent builds the event of the given kind. A kind this version does not
// know becomes a Raw that holds the non-envelope members as received.
func decodeEvent(kind string, line []byte, ms []member) (Event, error) {
	if key, ok := requiredObject[kind]; ok {
		if raw, ok := find(ms, key); !ok || string(raw) == "null" {
			return nil, fmt.Errorf("wire: %s: %q is missing or null", kind, key)
		}
	}
	var (
		ev  Event
		err error
	)
	switch kind {
	case "hello":
		ev, err = decodeAs[Hello](line)
	case "snapshot":
		ev, err = decodeAs[Snapshot](line)
	case "agent.upsert":
		ev, err = decodeAs[AgentUpsert](line)
	case "agent.status":
		ev, err = decodeAs[AgentStatusChange](line)
	case "tool.start":
		ev, err = decodeAs[ToolStart](line)
	case "tool.update":
		ev, err = decodeAs[ToolUpdate](line)
	case "tool.output":
		ev, err = decodeAs[ToolOutput](line)
	case "tool.end":
		ev, err = decodeAs[ToolEnd](line)
	case "fs.change":
		ev, err = decodeAs[FsChange](line)
	case "source.status":
		ev, err = decodeAs[SourceStatus](line)
	case "coverage":
		ev, err = decodeAs[CoverageReport](line)
	default:
		return rawEvent(kind, ms)
	}
	if err != nil {
		return nil, fmt.Errorf("wire: %s: %w", kind, err)
	}
	return ev, nil
}

func rawEvent(kind string, ms []member) (Event, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for _, m := range ms {
		if envelopeKeys[m.key] {
			continue
		}
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		k, err := marshalValue(m.key)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(m.val)
	}
	buf.WriteByte('}')
	return Raw{KindName: kind, Fields: buf.Bytes()}, nil
}

// DecodeSourceEvent decodes an event object that has kind and ts and no
// envelope fields. The golden files and the collector's state use this form.
func DecodeSourceEvent(line []byte) (SourceEvent, error) {
	ms, err := scanObject(line)
	if err != nil {
		return SourceEvent{}, err
	}
	kind, err := stringField(ms, "kind")
	if err != nil {
		return SourceEvent{}, err
	}
	ts, err := stringField(ms, "ts")
	if err != nil {
		return SourceEvent{}, err
	}
	ev, err := decodeEvent(kind, line, ms)
	if err != nil {
		return SourceEvent{}, err
	}
	return SourceEvent{Ts: ts, Ev: ev}, nil
}

// DecodeEnvelope decodes one NDJSON line. The line must be a JSON object with
// v equal to 1, a string kind, a string host, an integer seq and a string ts.
// A known kind whose fields have the wrong type is an error, and so is an
// agent.upsert, tool.start or coverage whose object is missing or null.
// Fields that a known kind does not define are dropped. An unknown kind
// becomes a Raw.
func DecodeEnvelope(line []byte) (Envelope, error) {
	ms, err := scanObject(line)
	if err != nil {
		return Envelope{}, err
	}
	v, err := intField(ms, "v")
	if err != nil {
		return Envelope{}, err
	}
	if v != SchemaVersion {
		return Envelope{}, fmt.Errorf("wire: unsupported version %d", v)
	}
	kind, err := stringField(ms, "kind")
	if err != nil {
		return Envelope{}, err
	}
	host, err := stringField(ms, "host")
	if err != nil {
		return Envelope{}, err
	}
	seq, err := intField(ms, "seq")
	if err != nil {
		return Envelope{}, err
	}
	ts, err := stringField(ms, "ts")
	if err != nil {
		return Envelope{}, err
	}
	ev, err := decodeEvent(kind, line, ms)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{V: SchemaVersion, Host: host, Seq: seq, Ts: ts, Ev: ev}, nil
}
