package schema

import "encoding/json"

// DecodeLine decodes one NDJSON line. It returns false for invalid JSON, a
// value that is not an object, or an envelope version other than 1.
// Unknown kinds decode without error.
func DecodeLine(line []byte) (Envelope, bool) {
	var e Envelope
	if err := json.Unmarshal(line, &e); err != nil {
		return Envelope{}, false
	}
	if e.V != Version {
		return Envelope{}, false
	}
	return e, true
}
