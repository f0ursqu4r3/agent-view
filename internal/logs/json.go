// Package logs reads agent log files: it discovers them, tails them and
// decodes their JSON records.
package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
)

// Entry is one key and value of an Obj.
type Entry struct {
	Key string
	Val any
}

// Obj is a decoded JSON object. It keeps the key order, which a Go map loses.
type Obj struct {
	keys []string
	vals map[string]any
}

func (o *Obj) set(key string, val any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = val
}

// Get returns the value of key, or nil when the key is absent (or null).
// It is safe to call on a nil Obj.
func (o *Obj) Get(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

// Entries returns the entries in JavaScript Object.entries order: keys that
// are canonical array indexes (0 to 4294967294) first, in ascending numeric
// order, then the other keys in insertion order. It is safe to call on a nil Obj.
func (o *Obj) Entries() []Entry {
	if o == nil || len(o.keys) == 0 {
		return nil
	}
	var index []uint32
	var rest []string
	for _, k := range o.keys {
		if n, ok := arrayIndex(k); ok {
			index = append(index, n)
		} else {
			rest = append(rest, k)
		}
	}
	slices.Sort(index)
	out := make([]Entry, 0, len(o.keys))
	for _, n := range index {
		k := strconv.FormatUint(uint64(n), 10)
		out = append(out, Entry{k, o.vals[k]})
	}
	for _, k := range rest {
		out = append(out, Entry{k, o.vals[k]})
	}
	return out
}

// arrayIndex reports whether key is a canonical array index: decimal digits
// with no leading zero, from 0 to 2^32-2.
func arrayIndex(key string) (uint32, bool) {
	if len(key) == 0 || len(key) > 10 || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(key, 10, 64)
	if err != nil || n >= math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true
}

// Decode parses one JSON value, as JSON.parse does. Objects become *Obj,
// arrays []any, numbers float64 (±Inf when out of range), plus string, bool
// and nil. A duplicate key keeps its first position and its last value. Any
// text after the value except JSON whitespace is an error.
func Decode(line string) (any, error) {
	if !json.Valid([]byte(line)) {
		return nil, errors.New("invalid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(line)))
	dec.UseNumber()
	return decodeValue(dec)
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			o := &Obj{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(keyTok.(string), val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		}
		arr := []any{}
		for dec.More() {
			val, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return arr, nil
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !math.IsInf(f, 0) {
			return nil, err
		}
		return f, nil
	}
	return tok, nil // string, bool or nil
}

// str returns v when it is a non-empty string.
func str(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

// obj returns v when it is an object, else nil.
func obj(v any) *Obj {
	o, _ := v.(*Obj)
	return o
}

// isInteger reports whether v is a number with an integer value that fits an
// int64 (Number.isInteger, without the values beyond 2^63).
func isInteger(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) >= 1<<63 {
		return 0, false
	}
	return int(f), true
}
