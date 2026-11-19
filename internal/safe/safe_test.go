package safe

import (
	"errors"
	"testing"
)

// Call returns nil when fn does not panic, and it runs fn once.
func TestCallWithoutPanic(t *testing.T) {
	n := 0
	if got := Call(func() { n++ }); got != nil {
		t.Errorf("Call = %v, want nil", got)
	}
	if n != 1 {
		t.Errorf("fn ran %d times, want 1", n)
	}
}

// Call recovers a panic and returns its value.
func TestCall(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name string
		val  any
	}{
		{"string", "oops"},
		{"error", boom},
		{"int", 7},
	}
	for _, c := range cases {
		if got := Call(func() { panic(c.val) }); got != c.val {
			t.Errorf("%s: Call = %v, want %v", c.name, got, c.val)
		}
	}
}

// panic(nil) is reported too (Go 1.21 and later turn it into a PanicNilError).
func TestCallPanicNil(t *testing.T) {
	if got := Call(func() { panic(nil) }); got == nil {
		t.Error("Call returned nil after panic(nil)")
	}
}

// A nil fn is a no-op.
func TestCallNilFunc(t *testing.T) {
	if got := Call(nil); got != nil {
		t.Errorf("Call(nil) = %v, want nil", got)
	}
}
