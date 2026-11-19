package wire

import (
	"bytes"
	"encoding/json"
	"errors"
)

// MarshalNoEscape encodes v as JSON without HTML escaping and without the
// trailing newline that json.Encoder adds. Use it, or EncodeLine for an
// Envelope, to write wire JSON. Never call json.Marshal on an Envelope: it
// escapes <, > and & again.
func MarshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// MarshalJSON writes one flat object. The keys are v, host, seq, kind, the
// event's fields in schema.ts order, and ts. Do not call json.Marshal on an
// Envelope to write it out: json.Marshal escapes <, > and & again, so the
// bytes differ from the TypeScript output. Use EncodeLine.
func (e Envelope) MarshalJSON() ([]byte, error) {
	if e.Ev == nil {
		return nil, errors.New("wire: envelope has no event")
	}
	body, err := MarshalNoEscape(e.Ev)
	if err != nil {
		return nil, err
	}
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, errors.New("wire: event does not encode as an object")
	}
	head := struct {
		V    int    `json:"v"`
		Host string `json:"host"`
		Seq  int64  `json:"seq"`
		Kind string `json:"kind"`
	}{e.V, e.Host, e.Seq, e.Ev.Kind()}
	h, err := MarshalNoEscape(head)
	if err != nil {
		return nil, err
	}
	ts, err := MarshalNoEscape(e.Ts)
	if err != nil {
		return nil, err
	}
	inner := body[1 : len(body)-1]
	out := make([]byte, 0, len(h)+len(inner)+len(ts)+10)
	out = append(out, h[:len(h)-1]...) // drop the closing brace
	if len(inner) > 0 {
		out = append(out, ',')
		out = append(out, inner...)
	}
	out = append(out, `,"ts":`...)
	out = append(out, ts...)
	out = append(out, '}')
	return out, nil
}

// EncodeLine returns the envelope as one JSON line that ends with a newline.
// It never escapes <, > or &. It panics when the envelope has no event, which
// is a programming error.
func EncodeLine(e Envelope) []byte {
	b, err := e.MarshalJSON()
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}
