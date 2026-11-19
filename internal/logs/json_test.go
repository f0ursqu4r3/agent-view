package logs

import (
	"math"
	"reflect"
	"slices"
	"testing"
)

func keysOf(o *Obj) []string {
	var keys []string
	for _, e := range o.Entries() {
		keys = append(keys, e.Key)
	}
	return keys
}

func mustDecode(t *testing.T, line string) any {
	t.Helper()
	v, err := Decode(line)
	if err != nil {
		t.Fatalf("Decode(%q): %v", line, err)
	}
	return v
}

func TestDecodeValues(t *testing.T) {
	o := mustDecode(t, `{"s":"x","n":1.5,"b":true,"z":null,"a":[1,"two",{"k":[]}],"o":{}}`).(*Obj)
	if o.Get("s") != "x" || o.Get("n") != 1.5 || o.Get("b") != true || o.Get("z") != nil {
		t.Errorf("scalars: %#v", o)
	}
	a := o.Get("a").([]any)
	if len(a) != 3 || a[0] != 1.0 || a[1] != "two" {
		t.Errorf("array: %#v", a)
	}
	if k := a[2].(*Obj).Get("k"); !reflect.DeepEqual(k, []any{}) {
		t.Errorf("empty array: %#v", k)
	}
	if got := o.Get("o").(*Obj).Entries(); len(got) != 0 {
		t.Errorf("empty object: %#v", got)
	}
	if o.Get("missing") != nil {
		t.Error("missing key should be nil")
	}
	if mustDecode(t, "null") != nil || mustDecode(t, "7") != 7.0 || mustDecode(t, `"s"`) != "s" || mustDecode(t, "false") != false {
		t.Error("top-level scalars")
	}
}

func TestDecodeNumberRange(t *testing.T) {
	if v := mustDecode(t, "1e999"); v != math.Inf(1) {
		t.Errorf("1e999: %v", v)
	}
	if v := mustDecode(t, "-1e999"); v != math.Inf(-1) {
		t.Errorf("-1e999: %v", v)
	}
	if v := mustDecode(t, "1e-999"); v != 0.0 {
		t.Errorf("1e-999: %v", v)
	}
	if v := mustDecode(t, "-0").(float64); v != 0 || !math.Signbit(v) {
		t.Errorf("-0: %v", v)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, line := range []string{"", " ", "{oops", `{"a":1,}`, "[1,]", "01", "'a'", "{}{}", "[] []", "\xef\xbb\xbf{}", "{}\v", "{} ", `"a`, "{\"a\":\"\x01\"}"} {
		if v, err := Decode(line); err == nil {
			t.Errorf("Decode(%q) = %#v, want an error", line, v)
		}
	}
}

func TestDecodeTrailingGarbage(t *testing.T) {
	if _, err := Decode("{} x"); err == nil {
		t.Error("{} x must be an error")
	}
	for _, line := range []string{"{}  ", "{} \t\r\n", " \t{}"} {
		if _, err := Decode(line); err != nil {
			t.Errorf("Decode(%q): %v", line, err)
		}
	}
}

func TestDecodeLoneSurrogate(t *testing.T) {
	if got := mustDecode(t, `"\ud800"`); got != "�" {
		t.Errorf("got %q", got)
	}
	if got := mustDecode(t, `"😀"`); got != "😀" {
		t.Errorf("pair: %q", got)
	}
}

// Review Focus 2: Object.entries order.
func TestDecodeEntriesOrder(t *testing.T) {
	o := mustDecode(t, `{"b.ts":1,"a.ts":2,"10":3,"2":4}`).(*Obj)
	if got, want := keysOf(o), []string{"2", "10", "b.ts", "a.ts"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	e := o.Entries()
	if e[0].Val != 4.0 || e[1].Val != 3.0 || e[2].Val != 1.0 {
		t.Errorf("values: %#v", e)
	}
}

func TestDecodeEntriesArrayIndexBoundary(t *testing.T) {
	o := mustDecode(t, `{"z":0,"4294967295":1,"4294967294":2,"01":3,"0":4,"-1":5,"1.5":6,"":7}`).(*Obj)
	want := []string{"0", "4294967294", "z", "4294967295", "01", "-1", "1.5", ""}
	if got := keysOf(o); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeDuplicateKey(t *testing.T) {
	o := mustDecode(t, `{"a":1,"b":2,"a":3}`).(*Obj)
	if got, want := keysOf(o), []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("keys %v", got)
	}
	if o.Get("a") != 3.0 {
		t.Errorf("a = %v", o.Get("a"))
	}
}

func TestNilObj(t *testing.T) {
	var o *Obj
	if o.Get("a") != nil || o.Entries() != nil {
		t.Error("nil Obj must be empty")
	}
}

func TestHelpers(t *testing.T) {
	if s, ok := str("x"); !ok || s != "x" {
		t.Error("str")
	}
	for _, v := range []any{"", 1.0, nil, true, []any{"x"}} {
		if _, ok := str(v); ok {
			t.Errorf("str(%#v) should fail", v)
		}
	}
	o := mustDecode(t, `{}`)
	if obj(o) == nil || obj([]any{}) != nil || obj("x") != nil || obj(nil) != nil {
		t.Error("obj")
	}
	if n, ok := isInteger(3.0); !ok || n != 3 {
		t.Error("isInteger 3")
	}
	if n, ok := isInteger(-2.0); !ok || n != -2 {
		t.Error("isInteger -2")
	}
	for _, v := range []any{1.5, math.Inf(1), math.NaN(), "3", nil, 1e30} {
		if _, ok := isInteger(v); ok {
			t.Errorf("isInteger(%#v) should fail", v)
		}
	}
}
